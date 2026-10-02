package web

import (
	"net/http"
	"sync"
	"time"
)

// Audio streaming (phase 4, approach A): the radio taps its post-chain
// mono audio; this hub resamples every input rate to a fixed 24 kHz and
// encodes µ-law (1 byte/sample ≈ 24 KB/s) for chunked HTTP delivery.
// The browser plays it back through an AudioWorklet ring buffer.

const audioOutRate = 24000

type audioListener struct {
	ch     chan []byte
	closed bool
}

// audioHub fans the encoded stream out to every connected browser.
type audioHub struct {
	mu        sync.Mutex
	listeners map[*audioListener]bool
	// resampler state (single producer: the DSP goroutine)
	inRate int
	pos    float64
	prev   float32
}

func newAudioHub() *audioHub {
	return &audioHub{listeners: map[*audioListener]bool{}}
}

// push consumes one block of demodulated mono audio at rate Hz.
func (h *audioHub) push(mono []float32, rate int) {
	h.mu.Lock()
	n := len(h.listeners)
	if n == 0 {
		h.mu.Unlock()
		return
	}
	if h.inRate != rate {
		h.inRate, h.pos, h.prev = rate, 0, 0
	}
	// Linear resample to audioOutRate.
	step := float64(rate) / audioOutRate
	out := make([]byte, 0, len(mono)*audioOutRate/rate+2)
	for _, s := range mono {
		for h.pos < 1 {
			v := h.prev + (s-h.prev)*float32(h.pos)
			out = append(out, ulawEncode(v))
			h.pos += step
		}
		h.pos -= 1
		h.prev = s
	}
	for l := range h.listeners {
		select {
		case l.ch <- out:
		default: // slow client: drop this block rather than block the DSP
		}
	}
	h.mu.Unlock()
}

func (h *audioHub) add() *audioListener {
	l := &audioListener{ch: make(chan []byte, 64)}
	h.mu.Lock()
	h.listeners[l] = true
	h.mu.Unlock()
	return l
}

func (h *audioHub) remove(l *audioListener) {
	h.mu.Lock()
	delete(h.listeners, l)
	h.mu.Unlock()
}

// ulawEncode maps a -1..1 float to an 8-bit µ-law code (ITU G.711).
func ulawEncode(v float32) byte {
	const bias = 0x84
	const clip = 32635
	s := int(v * 32767)
	sign := 0
	if s < 0 {
		sign, s = 0x80, -s
	}
	if s > clip {
		s = clip
	}
	s += bias
	exp := 7
	for mask := 0x4000; mask != 0 && s&mask == 0; mask >>= 1 {
		exp--
	}
	mant := (s >> (uint(exp) + 3)) & 0x0F
	return ^(byte(sign) | byte(exp<<4) | byte(mant))
}

// handleAudio streams µ-law bytes until the client disconnects.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// Tiny header: magic + sample rate, so the client can verify.
	w.Write([]byte{'U', 'L', 'A', 'W', byte(audioOutRate >> 8), byte(audioOutRate & 0xFF)})
	fl.Flush()

	l := s.audio.add()
	defer s.audio.remove(l)
	for {
		select {
		case <-r.Context().Done():
			return
		case blk := <-l.ch:
			if _, err := w.Write(blk); err != nil {
				return
			}
			fl.Flush()
		case <-time.After(5 * time.Second):
			// keepalive so intermediaries do not reap idle streams
			if _, err := w.Write(nil); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
