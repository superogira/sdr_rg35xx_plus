// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package deepcw

import (
	"math"
	"sync/atomic"
	"testing"
	"time"
)

// TestNilEngineNoOp: every method on a nil *Engine must be a safe no-op
// (the feature is off or the sidecar missing on most installs).
func TestNilEngineNoOp(t *testing.T) {
	var e *Engine
	e.Feed([]float64{1, 2, 3})
	if got := e.Text(); got != "" {
		t.Fatalf("nil Text = %q", got)
	}
	e.Clear()
	if e.Alive() {
		t.Fatal("nil engine reports alive")
	}
	e.Close()
}

// TestResampleRatio: the 8k->3.2k writer must emit exactly 0.4 output
// samples per input sample over a long feed (ratio 2.5), with no drift.
func TestResampleRatio(t *testing.T) {
	var got int64
	e := &Engine{q: make(chan []float64, 512), quit: make(chan struct{}), maxRunes: 100}
	e.in = countWriter{&got}
	done := make(chan struct{})
	go func() {
		e.writeLoop()
		close(done)
	}()

	const nChunks = 200
	chunk := make([]float64, 800) // 0.1 s at 8 kHz
	for i := range chunk {
		chunk[i] = math.Sin(2 * math.Pi * 700 * float64(i) / 8000)
	}
	for c := 0; c < nChunks; c++ {
		e.Feed(chunk)
	}
	e.Close() // closes quit; writer drains and exits
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writeLoop did not exit on Close")
	}
	inSamples := nChunks * len(chunk)
	want := int64(float64(inSamples) / 2.5)
	if got < want-4 || got > want+1 {
		t.Fatalf("emitted %d s16 samples for %d inputs, want ~%d (ratio drift)", got, inSamples, want)
	}
}

type countWriter struct{ n *int64 }

func (w countWriter) Write(p []byte) (int, error) {
	atomic.AddInt64(w.n, int64(len(p)/2)) // s16 samples
	return len(p), nil
}
func (w countWriter) Close() error { return nil }

// TestAvailableMissing: Available must be false in an empty dir.
func TestAvailableMissing(t *testing.T) {
	if Available(t.TempDir()) {
		t.Fatal("Available true in empty dir")
	}
}
