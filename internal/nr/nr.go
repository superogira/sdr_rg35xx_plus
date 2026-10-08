// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package nr drives the hamnoise sidecar (HamNoise neural noise
// reduction, AGPL-3.0 — kept out of this binary by running it as a
// separate process, like the rtl_tcp sidecar). Protocol: float32 mono
// at 9600 Hz both ways, exactly one 144-sample hop out per hop in.
package nr

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

const (
	nrRate = 9600
	hop    = 144
)

// Engine is one running sidecar process. A nil *Engine is a valid
// no-op (feature off or binary missing).
type Engine struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.Reader
	mode string
	dead bool

	inBuf  []float32 // rate-domain samples awaiting resample+send
	outBuf []float32 // 9600-domain samples awaiting resample back
	inPos  float64   // fractional read position, rate -> 9600
	outPos float64   // fractional read position, 9600 -> rate
}

// Start launches the sidecar from the first location that has it.
// Returns nil when the binary is missing (feature unavailable).
func Start(exeDir, mode string) *Engine {
	cands := []string{filepath.Join(exeDir, "hamnoise"), "/root/hamnoise"}
	if p, err := exec.LookPath("hamnoise"); err == nil {
		cands = append(cands, p)
	}
	var path string
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			path = c
			break
		}
	}
	if path == "" {
		return nil
	}
	cmd := exec.Command(path, mode)
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
	return &Engine{cmd: cmd, in: stdin, out: stdout, mode: mode}
}

// Available reports whether the sidecar binary exists at any known
// location (so the menu/web can grey the option out).
func Available(exeDir string) bool {
	for _, c := range []string{filepath.Join(exeDir, "hamnoise"), "/root/hamnoise"} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return true
		}
	}
	if _, err := exec.LookPath("hamnoise"); err == nil {
		return true
	}
	return false
}

// newEngineWithPipes builds an Engine over caller-supplied pipes
// (tests substitute an in-memory sidecar).
func newEngineWithPipes(in io.WriteCloser, out io.Reader, mode string) *Engine {
	return &Engine{in: in, out: out, mode: mode}
}

// Mode reports the model in use ("voice" or "cw").
func (e *Engine) Mode() string {
	if e == nil {
		return ""
	}
	return e.mode
}

// Process denoises one block of audio at the given rate. Any pipe
// failure marks the engine dead and passes audio through unchanged.
func (e *Engine) Process(in []float32, rate int) []float32 {
	if e == nil || len(in) == 0 {
		return in
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dead {
		return in
	}
	// Resample rate -> 9600, APPENDING to the unsent remainder — the
	// hop quantization leaves a fractional residue that must survive
	// to the next block or the pipe drifts out of 1:1 lock.
	e.inBuf = append(e.inBuf, resampleInto(nil, in, float64(rate)/nrRate, &e.inPos)...)
	// Feed whole hops; the sidecar answers one hop per hop.
	sent := 0
	hopBytes := make([]byte, hop*4)
	for len(e.inBuf)-sent*hop >= hop {
		f32toLE(e.inBuf[sent*hop:(sent+1)*hop], hopBytes)
		if _, err := e.in.Write(hopBytes); err != nil {
			e.die()
			return in
		}
		sent++
	}
	e.inBuf = append(e.inBuf[:0], e.inBuf[sent*hop:]...)
	if sent == 0 {
		return in
	}
	// Read exactly `sent` hops back.
	got := make([]float32, 0, sent*hop)
	for i := 0; i < sent; i++ {
		if _, err := io.ReadFull(e.out, hopBytes); err != nil {
			e.die()
			return in
		}
		got = append(got, leToF32(hopBytes)...)
	}
	e.outBuf = append(e.outBuf[:0], got...)
	// Resample 9600 -> rate for the output path.
	return resampleInto(nil, e.outBuf, nrRate/float64(rate), &e.outPos)
}

// Close stops the sidecar.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.in != nil {
		e.in.Close()
	}
	if e.cmd != nil && e.cmd.Process != nil {
		e.cmd.Process.Kill()
	}
	e.dead = true
}

func (e *Engine) die() {
	e.dead = true
	if e.cmd != nil && e.cmd.Process != nil {
		e.cmd.Process.Kill()
	}
}

// resampleInto linearly resamples in (ratio = inRate/outRate samples
// consumed per output sample) appending to dst; pos carries the
// fractional read position across calls.
func resampleInto(dst, in []float32, ratio float64, pos *float64) []float32 {
	n := len(in)
	for *pos < float64(n-1) {
		i := int(*pos)
		f := *pos - float64(i)
		dst = append(dst, in[i]+float32(f)*(in[i+1]-in[i]))
		*pos += ratio
	}
	// Carry the fractional remainder (possibly negative) into the next
	// block — clamping it to zero loses ~ratio samples per call, which
	// drifts the pipe out of 1:1 lock with the sidecar.
	*pos -= float64(n)
	return dst
}

func f32toLE(v []float32, b []byte) {
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
}

func leToF32(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}
