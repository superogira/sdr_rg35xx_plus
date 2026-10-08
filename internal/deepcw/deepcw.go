// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package deepcw drives the deepcw sidecar (DeepCW neural Morse
// decoder, AGPL-3.0-only — model and ONNX Runtime stay in the separate
// process, like the hamnoise sidecar). Protocol: mono s16 PCM at
// 3200 Hz into stdin; one text line per decode window out of stdout.
package deepcw

import (
	"bufio"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Engine is one running sidecar process. A nil *Engine is a valid no-op
// (feature off or sidecar/model missing).
type Engine struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	dead bool
	once sync.Once

	q        chan []float64 // 8k-domain chunks awaiting resample+send
	quit     chan struct{}
	text     strings.Builder
	maxRunes int

	// writer-goroutine state
	inBuf []float64
	pos   float64 // fractional read position, 8000 -> 3200
}

// Start launches the sidecar next to the app binary. Returns nil when
// the binary or the model is missing (feature unavailable).
func Start(exeDir string, threads, window int) *Engine {
	bin := filepath.Join(exeDir, "deepcw")
	model := filepath.Join(exeDir, "deepcw-model.onnx")
	meta := filepath.Join(exeDir, "deepcw-model.onnx.json")
	for _, f := range []string{bin, model, meta} {
		if st, err := os.Stat(f); err != nil || st.IsDir() {
			return nil
		}
	}
	// ORT libs ship beside the binary; LD_LIBRARY_PATH picks them up.
	cmd := exec.Command(bin, model, meta,
		strconv.Itoa(threads), strconv.Itoa(window), "1")
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+exeDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil
	}
	e := &Engine{cmd: cmd, in: stdin, q: make(chan []float64, 64), quit: make(chan struct{}), maxRunes: 20000}
	go e.readLoop(stdout)
	go e.writeLoop()
	go func() {
		_ = cmd.Wait()
		e.mu.Lock()
		e.dead = true
		e.mu.Unlock()
	}()
	return e
}

// Available reports whether the sidecar binary and model are present.
func Available(exeDir string) bool {
	for _, f := range []string{"deepcw", "deepcw-model.onnx", "deepcw-model.onnx.json"} {
		if st, err := os.Stat(filepath.Join(exeDir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

func (e *Engine) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "READY") {
			continue
		}
		e.mu.Lock()
		if e.text.Len() > 0 {
			e.text.WriteByte(' ')
		}
		e.text.WriteString(line)
		if r := []rune(e.text.String()); len(r) > e.maxRunes {
			e.text.Reset()
			e.text.WriteString(string(r[len(r)-e.maxRunes:]))
		}
		e.mu.Unlock()
	}
}

// Feed queues 8 kHz monitor audio (the CW branch tap). It never blocks:
// a full queue drops the chunk (the sidecar fell behind; CW repeats).
func (e *Engine) Feed(x []float64) {
	if e == nil {
		return
	}
	cp := append([]float64(nil), x...)
	select {
	case e.q <- cp:
	default:
	}
}

// writeLoop resamples queued 8 kHz audio to 3.2 kHz s16 and writes it
// to the sidecar's stdin (off the audio/DSP goroutine).
func (e *Engine) writeLoop() {
	for {
		var x []float64
		select {
		case x = <-e.q:
		case <-e.quit:
			// drain what is already queued so a Close right after the
			// last Feed does not drop it, then release the pipe
		drain:
			for {
				select {
				case x = <-e.q:
					e.push(x)
					e.flush()
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
		e.push(x)
		e.flush()
	}
}

func (e *Engine) push(x []float64) {
	e.mu.Lock()
	e.inBuf = append(e.inBuf, x...)
	e.mu.Unlock()
}

func (e *Engine) flush() {
	e.mu.Lock()
	if len(e.inBuf) < 4 {
		e.mu.Unlock()
		return
	}
	// linear resample 8000 -> 3200 (ratio 2.5)
	n := int((float64(len(e.inBuf)) - 1 - e.pos) / 2.5)
	if n <= 0 {
		e.mu.Unlock()
		return
	}
	pcm := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		p := e.pos + float64(i)*2.5
		i0 := int(p)
		f := p - float64(i0)
		v := e.inBuf[i0]*(1-f) + e.inBuf[i0+1]*f
		s := int(math.Round(v * 32767))
		if s > 32767 {
			s = 32767
		}
		if s < -32768 {
			s = -32768
		}
		pcm = append(pcm, byte(s&0xFF), byte(s>>8))
	}
	e.pos += float64(n) * 2.5
	// keep from the sample the next interpolation starts at; the new
	// fractional position stays in [0,1) (overshooting makes pos
	// negative and the next read indexes below zero)
	keep := int(e.pos)
	if keep > len(e.inBuf) {
		keep = len(e.inBuf)
	}
	e.inBuf = append(e.inBuf[:0], e.inBuf[keep:]...)
	e.pos -= float64(keep)
	in := e.in
	e.mu.Unlock()
	_, _ = in.Write(pcm)
}

// Text returns the rolling decoded text.
func (e *Engine) Text() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.text.String()
}

// Clear drops the rolling text.
func (e *Engine) Clear() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.text.Reset()
	e.mu.Unlock()
}

// Alive reports whether the sidecar process is still running.
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
		close(e.quit) // writer drains, closes the pipe and exits
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
}
