//go:build linux

// Package input reads the RG35XX gamepad straight from evdev, the way the
// working apps on this firmware do it (net_radio's InputHandler): find the
// input device whose name contains "ANBERNIC", read 24-byte input_event
// records, map key/axis codes to logical buttons.
//
// The device is read by a dedicated goroutine: a blocking os.File.Read on
// an evdev node waits in the Go netpoller until a button event arrives
// (calling SetNonblock on the raw fd does NOT change that — the poller owns
// the readiness wait), so the UI loop must never read the fd inline. It
// drains the event channel instead, which never blocks.
package input

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"
)

// Button is a logical app button.
type Button int

const (
	Up Button = iota
	Down
	Left
	Right
	A
	B
	X
	Y
	L1
	R1
	L2
	R2
	Select
	Start
	Menu
	VolDown
	VolUp
)

var keyNames = map[Button]string{
	Up: "UP", Down: "DOWN", Left: "LEFT", Right: "RIGHT",
	A: "A", B: "B", X: "X", Y: "Y",
	L1: "L1", R1: "R1", L2: "L2", R2: "R2",
	Select: "SELECT", Start: "START", Menu: "MENU",
	VolDown: "VOL-", VolUp: "VOL+",
}

func (b Button) String() string {
	if s, ok := keyNames[b]; ok {
		return s
	}
	return fmt.Sprintf("BTN(%d)", int(b))
}

// evdev codes observed on the ANBERNIC H700 pads (values from the net_radio
// KEYMAP plus the usual KEY_/BTN_DPAD_ aliases for other firmware builds).
var codeToButton = map[uint16]Button{
	304: A, 305: B, 307: X, 306: Y,
	308: L1, 309: R1, 314: L2, 315: R2,
	310: Select, 311: Start, 312: Menu,
	114: VolDown, 115: VolUp,
	103: Up, 108: Down, 105: Left, 106: Right, // KEY_UP/…
	517: Up, 516: Down, 514: Left, 515: Right, // BTN_DPAD_*
}

const (
	evKey = 0x01
	evAbs = 0x03
	absX  = 16
	absY  = 17
)

// Event is one button state change.
type Event struct {
	Button Button
	Down   bool
}

// Reader owns the evdev fd and a background read loop. The pad reports its
// stick as ABS_X/ABS_Y with value −1/0/1, folded into the same logical
// directions.
type Reader struct {
	dev     *os.File
	events  chan Event
	closed  atomic.Bool
	pending []Event
	// axisDown is owned by the read goroutine.
	axisDown map[Button]bool
	// menuEchoUntil suppresses the firmware's phantom SELECT after MENU.
	menuEchoUntil time.Time
}

// Open finds and opens the ANBERNIC gamepad and starts its read loop. It
// returns an error if no pad is found (dev machines); the app continues
// without input then.
func Open() (*Reader, error) {
	path, err := findDevice()
	if err != nil {
		return nil, err
	}
	dev, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	r := &Reader{
		dev:      dev,
		events:   make(chan Event, 128),
		axisDown: map[Button]bool{},
	}
	go r.readLoop(path)
	return r, nil
}

func findDevice() (string, error) {
	matches, _ := filepath.Glob("/dev/input/event*")
	for _, p := range matches {
		if strings.Contains(strings.ToUpper(devName(p)), "ANBERNIC") {
			return p, nil
		}
	}
	// Fallback: the first event node (stock boards have exposed the pad
	// under other names too).
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("no /dev/input/event* nodes")
}

func devName(path string) string {
	base := filepath.Base(path)
	b, err := os.ReadFile(filepath.Join("/sys/class/input", base, "device", "name"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// input_event on 64-bit Linux: struct timeval (2×int64) + uint16 type +
// uint16 code + int32 value = 24 bytes.
type rawEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

const rawEventSize = int(unsafe.Sizeof(rawEvent{}))

// readLoop blocks on the device forever and converts records to Events.
// If the system daemon (gptokeyb-style) holds an exclusive EVIOCGRAB, no
// events ever arrive — the loop just sleeps, which is harmless.
func (r *Reader) readLoop(path string) {
	buf := make([]byte, 64*rawEventSize)
	for !r.closed.Load() {
		n, err := r.dev.Read(buf)
		if n > 0 {
			for off := 0; off+rawEventSize <= n; off += rawEventSize {
				e := (*rawEvent)(unsafe.Pointer(&buf[off]))
				r.handle(e)
			}
		}
		if err != nil {
			if r.closed.Load() || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				return
			}
			// Unexpected error (device vanished): back off instead of
			// spinning.
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (r *Reader) handle(e *rawEvent) {
	// This firmware emits a phantom SELECT press right after every MENU
	// release (observed in goro's input bring-up too). Since MENU is
	// screen-capture here, swallow the echo so capturing does not also
	// flip the demod mode.
	if e.Type == evKey && e.Code == 312 {
		r.menuEchoUntil = time.Now().Add(400 * time.Millisecond)
	}
	if e.Type == evKey && e.Code == 310 && time.Now().Before(r.menuEchoUntil) {
		return
	}
	switch e.Type {
	case evKey:
		if e.Value != 1 && e.Value != 0 {
			return // key repeats (2) are ignored; the app does its own repeat
		}
		if b, ok := codeToButton[e.Code]; ok {
			r.push(Event{Button: b, Down: e.Value == 1})
		}
	case evAbs:
		switch e.Code {
		case absX:
			r.axis(Left, e.Value < 0)
			r.axis(Right, e.Value > 0)
		case absY:
			r.axis(Up, e.Value < 0)
			r.axis(Down, e.Value > 0)
		}
	}
}

func (r *Reader) push(e Event) {
	select {
	case r.events <- e:
	default: // overflow drops the oldest-unread event rather than blocking
	}
}

func (r *Reader) axis(b Button, down bool) {
	if r.axisDown[b] == down {
		return
	}
	r.axisDown[b] = down
	r.push(Event{Button: b, Down: down})
}

// Poll drains everything the read loop has queued. It never blocks.
func (r *Reader) Poll() {
	for {
		select {
		case e := <-r.events:
			r.pending = append(r.pending, e)
		default:
			return
		}
	}
}

// Events drains the pending queue.
func (r *Reader) Events() []Event {
	ev := r.pending
	r.pending = nil
	return ev
}

func (r *Reader) Close() {
	r.closed.Store(true)
	if r.dev != nil {
		r.dev.Close()
	}
}
