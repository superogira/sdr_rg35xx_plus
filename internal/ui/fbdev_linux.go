//go:build linux

package ui

import (
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
	fbioPanDisplay     = 0x4606
)

// fbDisplay mmaps /dev/fb0 and converts the RGBA frame into the panel's
// format on present.
//
// Two StockOS facts drive this file (both learned the hard way and both
// confirmed by goro's gogpu fbdev backend):
//
//   - The framebuffer is double-buffered (virtual 640×960) and the fb
//     layer in the display engine stays OFF until an FBIOPAN/FBIOPUT
//     "touches the mode" — plain mmap writes are invisible until then
//     (that is why the app stayed behind the launcher's Loading screen:
//     our writes were in memory but never scanned out; the layer only
//     woke when exit-time cleanup touched fb0, flashing our last frame).
//     Fix: right after reading the mode, FBIOPAN with the UNMODIFIED
//     var_screeninfo buffer — same yoffset, just a mode touch.
//   - Every frame is mirrored into both halves of the virtual screen so
//     whichever half the panel scans out shows our pixels.
type fbDisplay struct {
	f      *os.File
	mem    []byte
	isFile bool
	w, h   int // visible size
	stride int
	base   int // byte offset of the vinfo-visible window
	mirror int // second virtual-screen copy, 0 = single buffered
	bpp    int
	// byte position of each channel inside a 32-bit pixel.
	rIdx, gIdx, bIdx, aIdx int
	sixteen                bool
	rBits, gBits, bBits    uint32
	rShift, gShift, bShift uint32

	// Periodic FBIOPAN re-activation (the frontend can re-pan over us).
	lastPan  time.Time
	openedAt time.Time
	rawVInfo [160]byte
}

func openFB() (Display, error) {
	path := envOr("SDR_FB", "/dev/fb0")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	d := &fbDisplay{w: 640, h: 480, bpp: 32, rIdx: 2, gIdx: 1, bIdx: 0, aIdx: 3}

	// Raw var_screeninfo: parsed by field, but the buffer itself is kept
	// untouched — the activation pan must pass the mode back exactly as
	// the driver gave it.
	var varRaw [160]byte
	var smemLen, lineLen int
	vinfoOK := ioctlOK(f, fbioGetVScreenInfo, unsafe.Pointer(&varRaw[0]))
	d.rawVInfo = varRaw // save for periodic re-pan
	if vinfoOK {
		w := int(binary.LittleEndian.Uint32(varRaw[0:]))
		h := int(binary.LittleEndian.Uint32(varRaw[4:]))
		yresV := int(binary.LittleEndian.Uint32(varRaw[12:]))
		xoff := int(binary.LittleEndian.Uint32(varRaw[16:]))
		yoff := int(binary.LittleEndian.Uint32(varRaw[20:]))
		bpp := int(binary.LittleEndian.Uint32(varRaw[24:]))
		rOff := int(binary.LittleEndian.Uint32(varRaw[32:]))
		rLen := int(binary.LittleEndian.Uint32(varRaw[36:]))
		gOff := int(binary.LittleEndian.Uint32(varRaw[44:]))
		gLen := int(binary.LittleEndian.Uint32(varRaw[48:]))
		bOff := int(binary.LittleEndian.Uint32(varRaw[56:]))
		bLen := int(binary.LittleEndian.Uint32(varRaw[60:]))
		if w > 0 && h > 0 {
			d.w, d.h, d.bpp = w, h, bpp
			switch {
			case bpp == 32:
				d.rIdx, d.gIdx, d.bIdx = rOff/8, gOff/8, bOff/8
				d.aIdx = 24 / 8
				if rOff == 0 && bOff == 0 {
					d.bIdx, d.gIdx, d.rIdx, d.aIdx = 0, 1, 2, 3
				}
			case bpp == 16:
				d.sixteen = true
				d.rBits, d.rShift = uint32(rLen), uint32(rOff)
				d.gBits, d.gShift = uint32(gLen), uint32(gOff)
				d.bBits, d.bShift = uint32(bLen), uint32(bOff)
			}
			d.base = yoff*(w*bpp/8) + xoff*bpp/8
			_ = yresV
		}
		var fixRaw [80]byte
		if ioctlOK(f, fbioGetFScreenInfo, unsafe.Pointer(&fixRaw[0])) {
			smemLen = int(binary.LittleEndian.Uint32(fixRaw[24:]))
			lineLen = int(binary.LittleEndian.Uint32(fixRaw[48:]))
		}
	} else if w, h, ok := parseVirtualSize(); ok {
		// sysfs fallback: only the combined "W,H" virtual_size exists.
		if h == 2*w*3/4 { // 960 == 2×480 for a 640 panel
			d.w, d.h = w, h/2
		} else {
			d.w, d.h = w, h
		}
	}

	// Overrides for off-device / odd panels.
	if v := os.Getenv("SDR_FB_W"); v != "" {
		d.w, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("SDR_FB_H"); v != "" {
		d.h, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("SDR_FB_BPP"); v != "" {
		d.bpp, _ = strconv.Atoi(v)
	}
	// The physical panel is 640x480 — some firmware states (SSH while
	// the frontend holds the display) report 1280x1024 which makes the
	// app render a 4x larger surface with expensive Thai text glyph
	// rasterization, freezing the UI for minutes on the A53.
	if d.w > 640 || d.h > 480 {
		d.w, d.h = 640, 480
	}

	d.stride = lineLen
	if d.stride == 0 {
		d.stride = d.w * d.bpp / 8
	}
	if v := os.Getenv("SDR_FB_STRIDE"); v != "" {
		d.stride, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("SDR_FB_BASE"); v != "" {
		d.base, _ = strconv.Atoi(v)
	}
	switch strings.ToLower(os.Getenv("SDR_FB_FORMAT")) {
	case "rgba":
		d.rIdx, d.gIdx, d.bIdx, d.aIdx = 0, 1, 2, 3
		d.sixteen = false
	case "bgra":
		d.rIdx, d.gIdx, d.bIdx, d.aIdx = 2, 1, 0, 3
		d.sixteen = false
	}

	// THE activation step: pan with the untouched mode. Until this
	// succeeds the fb layer is not scanned out on StockOS.
	if vinfoOK {
		if err := ioctlRaw(f, fbioPanDisplay, unsafe.Pointer(&varRaw[0])); err != nil {
			fmt.Fprintf(os.Stderr, "fbdev: FBIOPAN activation failed: %v (screen may stay blank)\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "fbdev: FBIOPAN activation ok — fb layer is live\n")
		}
	}

	mapLen := smemLen
	if mapLen < d.stride*(d.h*2) {
		mapLen = d.stride * d.h * 2
	}
	d.mirror = 0
	if mapLen >= d.stride*d.h*2 && d.base < d.stride*d.h {
		d.mirror = d.stride * d.h // second half
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice != 0 {
		mem, err := unix.Mmap(int(f.Fd()), 0, mapLen,
			unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil && mapLen > d.stride*d.h {
			mapLen = d.stride * d.h
			d.mirror = 0
			mem, err = unix.Mmap(int(f.Fd()), 0, mapLen,
				unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		}
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("mmap %s: %w", path, err)
		}
		d.mem = mem
	} else {
		mem := make([]byte, mapLen)
		if _, err := f.ReadAt(mem, 0); err != nil && err.Error() != "EOF" {
			_ = err // empty file is fine — writes create the image
		}
		d.mem = mem
		d.isFile = true
	}
	d.openedAt = time.Now()
	d.lastPan = d.openedAt
	fmt.Fprintf(os.Stderr, "fbdev: %dx%d bpp=%d stride=%d base=%d mirror=%d rgb=%d/%d/%d/%d sixteen=%v\n",
		d.w, d.h, d.bpp, d.stride, d.base, d.mirror, d.rIdx, d.gIdx, d.bIdx, d.aIdx, d.sixteen)
	return d, nil
}

// ioctlRaw runs an ioctl through the file's SyscallConn (the correct path
// for descriptors the Go netpoller registered; a bare syscall on File.Fd()
// produced EBADF here once).
func ioctlRaw(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	if err := rc.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
		if errno != 0 {
			ioctlErr = errno
		}
	}); err != nil {
		return err
	}
	return ioctlErr
}

// ioctlOK reports whether the ioctl ran without an error code.
func ioctlOK(f *os.File, req uintptr, arg unsafe.Pointer) bool {
	return ioctlRaw(f, req, arg) == nil
}

func parseVirtualSize() (w, h int, ok bool) {
	// /sys/class/graphics/fb0/virtual_size reads "640,960".
	b, err := os.ReadFile("/sys/class/graphics/fb0/virtual_size")
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimSpace(string(b)), ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	return w, h, err1 == nil && err2 == nil
}

func (d *fbDisplay) Size() (int, int) { return d.w, d.h }

func (d *fbDisplay) Present(frame *image.RGBA) error {
	if frame.Bounds().Dx() != d.w || frame.Bounds().Dy() != d.h {
		return fmt.Errorf("frame %v does not match fb %dx%d", frame.Bounds(), d.w, d.h)
	}
	// Re-assert the display layer every 5 s — the console frontend can
	// re-pan or overwrite after our app starts, freezing the screen on
	// the boot frame. This keeps the layer pointed at our buffer.
	interval := 2 * time.Second
	if time.Since(d.openedAt) < 30*time.Second {
		interval = 500 * time.Millisecond
	}
	if time.Since(d.lastPan) > interval {
		d.lastPan = time.Now()
		v := d.rawVInfo
		_ = ioctlRaw(d.f, fbioPanDisplay, unsafe.Pointer(&v[0]))
	}
	d.paint(frame, d.base)
	if d.mirror != 0 {
		d.paint(frame, d.mirror)
	}
	if d.isFile {
		if _, err := d.f.WriteAt(d.mem, 0); err != nil {
			return err
		}
	}
	return nil
}

func (d *fbDisplay) paint(frame *image.RGBA, base int) {
	src := frame.Pix
	for y := 0; y < d.h; y++ {
		srow := src[y*frame.Stride:]
		drow := d.mem[base+y*d.stride:]
		switch {
		case d.sixteen:
			for x := 0; x < d.w; x++ {
				r, g, b := uint32(srow[4*x]), uint32(srow[4*x+1]), uint32(srow[4*x+2])
				p := (r >> (8 - d.rBits)) << d.rShift
				p |= (g >> (8 - d.gBits)) << d.gShift
				p |= (b >> (8 - d.bBits)) << d.bShift
				drow[2*x] = byte(p)
				drow[2*x+1] = byte(p >> 8)
			}
		case d.bpp == 32:
			for x := 0; x < d.w; x++ {
				o := 4 * x
				drow[o+d.bIdx] = srow[o+2] // source B → panel blue byte
				drow[o+d.gIdx] = srow[o+1] // G
				drow[o+d.rIdx] = srow[o+0] // source R → panel red byte
				drow[o+d.aIdx] = 0xFF
			}
		}
	}
}

func (d *fbDisplay) Close() error {
	if d.mem != nil && !d.isFile {
		unix.Munmap(d.mem)
	}
	return d.f.Close()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
