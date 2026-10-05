package radio

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"sdr35/internal/dsp"
)

// recSink records every write and rate change; thread-safe.
type recSink struct {
	mu     sync.Mutex
	writes [][]float32
	rates  []int
}

func (r *recSink) WriteAudio(m []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := append([]float32(nil), m...)
	r.writes = append(r.writes, cp)
}
func (r *recSink) SetInputRate(rate int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rates = append(r.rates, rate)
}
func (r *recSink) Silence(ms int) {}

// The beacon must reach the speaker as ONE contiguous write at 48k,
// with the input rate swapped and restored — and the demo/receive
// audio must stay ducked while it plays (no interleaved writes).
func TestBeaconPlaysWithHostOff(t *testing.T) {
	dsp.SetIQRate(1_024_000)
	sink := &recSink{}
	r := NewDemo(dsp.ModeNFM, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	time.Sleep(200 * time.Millisecond) // let the demo settle

	// A 0.5 s beacon at 48k.
	beacon := make([]float64, 24000)
	for i := range beacon {
		beacon[i] = 0.5 * math.Sin(2*math.Pi*1200*float64(i)/48000)
	}
	r.PlayBeacon(beacon)
	if !r.BeaconPlaying() {
		t.Fatal("BeaconPlaying must be true right after PlayBeacon")
	}
	// Wait for the whole beacon to drain through the sink.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		done := false
		for _, w := range sink.writes {
			if len(w) == len(beacon) {
				done = true
			}
		}
		sink.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var got []float32
	for _, w := range sink.writes {
		if len(w) == len(beacon) {
			got = w
		}
	}
	if got == nil {
		t.Fatalf("beacon never reached the sink (writes=%d)", len(sink.writes))
	}
	// Content intact.
	for i := 0; i < len(beacon); i += 997 {
		if math.Abs(float64(got[i])-beacon[i]) > 1e-6 {
			t.Fatalf("sample %d corrupted: %v vs %v", i, got[i], beacon[i])
		}
	}
	// Rate swap: 48000 then back to the mode rate.
	if len(sink.rates) < 2 || sink.rates[0] != 48000 {
		t.Fatalf("rate sequence = %v, want [48000, ...]", sink.rates)
	}
	if last := sink.rates[len(sink.rates)-1]; last == 48000 {
		t.Fatal("input rate not restored after the beacon")
	}
	if r.BeaconPlaying() {
		t.Fatal("BeaconPlaying must clear once drained")
	}
}
