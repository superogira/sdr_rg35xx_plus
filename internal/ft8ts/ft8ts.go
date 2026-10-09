// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package ft8ts drives the ft8ts sidecar (GPL-3.0 TypeScript port of
// the WSJT-X v3.0.1 FT8 decoder, run under Node in a separate process —
// same isolation pattern as the hamnoise/deepcw sidecars). Protocol:
// mono float32le at the capture-side monitor rate into stdin; one JSON
// line per decoded message out of stdout.
package ft8ts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Msg is one decoded transmission as reported by the sidecar.
type Msg struct {
	Slot int64   `json:"slot"`
	Freq float64 `json:"freq"`
	SNR  float64 `json:"snr"`
	Text string  `json:"msg"`
	Kind int     `json:"kind"`
	Ms   int     `json:"ms"`
	N    int     `json:"n"` // quiet-slot heartbeat
}

// Engine is one running sidecar process. A nil *Engine is a no-op.
type Engine struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	quit chan struct{}
	once sync.Once
	dead bool

	q       chan []float64
	dropped int64   // chunks lost to a full queue (atomic)
	fedRMS  float64 // running RMS of audio the tap handed us
	fedN    int64   // samples counted into fedRMS
	msgs    []Msg   // undrained decodes
}

// Rev is the sidecar revision this build expects (written into the
// bundle's ft8ts.rev by tools/build_ft8ts_bundle.sh). A stale bundle —
// like the first sidecar, which deadlocked on stream gaps — re-downloads
// on the next enable instead of running the broken code forever.
const Rev = "r8"

// nodePath finds the bundled runtime: plain name on Linux (the only
// production target), .exe alongside for dev machines on Windows.
func nodePath(dir string) string {
	p := filepath.Join(dir, "ft8ts-node")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		if pe := p + ".exe"; func() bool { _, err := os.Stat(pe); return err == nil }() {
			return pe
		}
	}
	return p
}

// Files the bundle installs next to the app binary.
func bundleFiles(dir string) []string {
	return []string{
		filepath.Join(dir, "ft8ts-node"),
		filepath.Join(dir, "ft8ts.mjs"),
		filepath.Join(dir, "ft8ts-worker-node.mjs"),
		filepath.Join(dir, "ft8ts_sidecar.mjs"),
		filepath.Join(dir, "ft8ts.rev"),
	}
}

// Available reports whether the sidecar bundle is installed and current.
func Available(dir string) bool {
	for _, f := range bundleFiles(dir) {
		if st, err := os.Stat(f); err != nil || st.IsDir() {
			return false
		}
	}
	rev, err := os.ReadFile(filepath.Join(dir, "ft8ts.rev"))
	return err == nil && strings.TrimSpace(string(rev)) == Rev
}

// Start launches node + sidecar. rate is the monitor audio rate in Hz.
// Failure reasons go to stderr (the device log) — a silent nil here
// once left "cannot enable" with nothing to diagnose.
func Start(dir string, rate, depth, threads, low, high int) *Engine {
	if !Available(dir) {
		fmt.Fprintf(os.Stderr, "ft8ts: bundle incomplete in %s\n", dir)
		return nil
	}
	node := nodePath(dir)
	side := filepath.Join(dir, "ft8ts_sidecar.mjs")
	// node's dynamic import() needs a file:// URL on Windows (a bare
	// C:\ path is an unsupported scheme); the URL form works on Linux
	// too.
	lib := "file://" + filepath.ToSlash(filepath.Join(dir, "ft8ts.mjs"))
	cmd := exec.Command(node, side, lib,
		strconv.Itoa(rate), strconv.Itoa(depth), strconv.Itoa(threads),
		strconv.Itoa(low), strconv.Itoa(high))
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ft8ts: stdin pipe: %v\n", err)
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ft8ts: stdout pipe: %v\n", err)
		return nil
	}
	// A deep decode can block ingestion for several seconds; the audio
	// arriving meanwhile must SURVIVE in the OS pipe buffer (the
	// sidecar resyncs only on sustained lag, so a draining backlog is
	// real signal). 1 MB holds ~30 s of 8 kHz float32.
	fmt.Fprintf(os.Stderr, "ft8ts: start depth=%d threads=%d band=%d-%d\n", depth, threads, low, high)
	enlargePipe(stdin)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "ft8ts: start node: %v\n", err)
		return nil
	}
	e := &Engine{cmd: cmd, in: stdin, quit: make(chan struct{}), q: make(chan []float64, 512)}
	go e.readLoop(stdout)
	go e.writeLoop()
	go e.statLoop()
	go func() {
		_ = cmd.Wait()
		e.mu.Lock()
		e.dead = true
		e.mu.Unlock()
	}()
	return e
}

// statLoop reports queue depth every 10 s so a starved audio path is
// visible in the device log (deep queue + drops = writer starved;
// empty queue with silent windows at the sidecar = tap not feeding).
func (e *Engine) statLoop() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.quit:
			return
		case <-t.C:
			e.mu.Lock()
			dead := e.dead
			qlen := len(e.q)
			e.mu.Unlock()
			if dead {
				return
			}
			e.mu.Lock()
			rms := math.Sqrt(e.fedRMS)
			e.fedRMS, e.fedN = 0, 0
			e.mu.Unlock()
			fmt.Fprintf(os.Stderr, "ft8ts: queue=%d/512 dropped=%d tapRMS=%.4f"+string(rune(10)), qlen, atomic.LoadInt64(&e.dropped), rms)
		}
	}
}

func (e *Engine) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		var m Msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Text == "" {
			continue // quiet-slot heartbeat
		}
		e.mu.Lock()
		e.msgs = append(e.msgs, m)
		if len(e.msgs) > 256 {
			e.msgs = e.msgs[len(e.msgs)-256:]
		}
		e.mu.Unlock()
	}
}

// Feed queues monitor audio (float64 mono, as the DSP tap provides).
// Never blocks.
func (e *Engine) Feed(x []float64) {
	if e == nil {
		return
	}
	// track what the tap actually delivers (silence here with signal
	// on the band = the tap/chain layer is at fault, not the pipe)
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	e.mu.Lock()
	e.fedRMS = (e.fedRMS*float64(e.fedN) + sum) / float64(e.fedN+int64(len(x)))
	e.fedN += int64(len(x))
	e.mu.Unlock()
	cp := append([]float64(nil), x...)
	select {
	case e.q <- cp:
	default:
		n := atomic.AddInt64(&e.dropped, 1)
		if n%256 == 1 {
			fmt.Fprintf(os.Stderr, "ft8ts: audio queue dropped %d chunks (sidecar starved)"+string(rune(10)), n)
		}
	}
}

func (e *Engine) writeLoop() {
	for {
		var x []float64
		select {
		case x = <-e.q:
		case <-e.quit:
		drain:
			for {
				select {
				case x = <-e.q:
					e.write(x)
				default:
					break drain
				}
			}
			e.mu.Lock()
			in := e.in
			e.mu.Unlock()
			if in != nil {
				in.Close()
			}
			return
		}
		e.write(x)
	}
}

func (e *Engine) write(x []float64) {
	e.mu.Lock()
	dead := e.dead
	in := e.in
	e.mu.Unlock()
	if dead || in == nil {
		return
	}
	buf := make([]byte, len(x)*4)
	for i, v := range x {
		bits := math.Float32bits(float32(v))
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	_, _ = in.Write(buf)
}

// Take drains decoded messages.
func (e *Engine) Take() []Msg {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.msgs
	e.msgs = nil
	return out
}

// Dropped reports how many audio chunks the queue rejected (a starved
// sidecar or a saturated CPU shows up here).
func (e *Engine) Dropped() int64 {
	if e == nil {
		return 0
	}
	return atomic.LoadInt64(&e.dropped)
}

// Alive reports whether the sidecar is still running.
func (e *Engine) Alive() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.dead
}

// Close stops the sidecar.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		e.mu.Lock()
		cmd := e.cmd
		e.dead = true
		e.mu.Unlock()
		close(e.quit)
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
}

// enlargePipe raises the OS pipe capacity so audio that arrives while a
// deep decode blocks ingestion is buffered by the kernel instead of
// dropped. Linux-only (F_SETPIPE_SZ); best effort — the default 64 KB
// still works, it just survives shorter stalls.
func enlargePipe(w io.WriteCloser) {
	f, ok := w.(*os.File)
	if !ok {
		return
	}
	_, _ = fcntlSetPipeSz(f.Fd(), 1<<20)
}
