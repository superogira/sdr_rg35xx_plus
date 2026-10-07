package nr

import (
	"io"
	"math"
	"os"
	"testing"
)

// resampleInto must round-trip a tone through rate→9600→rate without
// drifting phase or amplitude (the pipe protocol depends on exact
// sample accounting).
func TestResampleRoundTrip(t *testing.T) {
	rate := 32000
	n := rate / 2 // half a second
	in := make([]float32, n)
	for i := range in {
		in[i] = float32(math.Sin(2 * math.Pi * 700 * float64(i) / float64(rate)))
	}
	var pos1, pos2 float64
	down := resampleInto(nil, in, float64(rate)/nrRate, &pos1)
	// 9600-domain length must be n*9600/rate ±2
	want := n * nrRate / rate
	if len(down) < want-2 || len(down) > want+2 {
		t.Fatalf("downsampled %d, want ~%d", len(down), want)
	}
	up := resampleInto(nil, down, nrRate/float64(rate), &pos2)
	if len(up) < n-4 || len(up) > n+4 {
		t.Fatalf("round trip %d samples, want ~%d", len(up), n)
	}
	// compare a mid segment (skip edges)
	var worst float64
	for i := n / 4; i < n/2; i++ {
		d := math.Abs(float64(up[i]) - float64(in[i]))
		if d > worst {
			worst = d
		}
	}
	if worst > 0.05 {
		t.Fatalf("round-trip error %.3f too large", worst)
	}
}

// A nil engine must pass audio through untouched.
func TestNilEnginePassthrough(t *testing.T) {
	var e *Engine
	in := []float32{1, 2, 3}
	if out := e.Process(in, 8000); len(out) != 3 || out[1] != 2 {
		t.Fatal("nil engine must pass through")
	}
	e.Close() // must not panic
}

// The engine must keep the pipe exactly 1:1 against a sidecar that
// answers one hop per hop, across odd block sizes and rate changes.
func TestEnginePipeProtocol(t *testing.T) {
	// os.Pipe has a kernel buffer like the real sidecar's stdin/stdout;
	// io.Pipe is unbuffered and would deadlock a write-then-read engine.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	or, ow, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	e := newEngineWithPipes(pw, or, "voice")
	// fake sidecar: echo hops back unchanged
	go func() {
		buf := make([]byte, hop*4)
		for {
			if _, err := io.ReadFull(pr, buf); err != nil {
				return
			}
			if _, err := ow.Write(buf); err != nil {
				return
			}
		}
	}()
	rate := 16000
	block := make([]float32, 1000) // odd size, not a hop multiple
	for i := range block {
		block[i] = float32(math.Sin(2 * math.Pi * 500 * float64(i) / float64(rate)))
	}
	var totalIn, totalOut int
	for k := 0; k < 20; k++ {
		out := e.Process(block, rate)
		totalIn += len(block)
		totalOut += len(out)
	}
	// echo sidecar: output length must track input within resample slack
	if d := totalIn - totalOut; d > 200 || d < -200 {
		t.Fatalf("pipe drift: in %d out %d", totalIn, totalOut)
	}
	e.Close()
}
