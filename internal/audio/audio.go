// Package audio pipes a raw s16le stereo stream into an external player.
//
// On the RG35XX StockOS the in-process audio libraries open a "working"
// context that never touches a kernel PCM stream (silent), while aplay and
// mpv play fine — and the speaker device opens exclusively, so exactly one
// player process must own it for the whole session (see goro's
// audio/pipe_output.go, which solved the same problem). This package keeps
// one player process alive and feeds it realtime-paced chunks, writing
// silence whenever the SDR has nothing new.
package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"sdr35/internal/dsp"
)

const (
	// SampleRate is what the player processes are told to run at. The
	// device codec is only known to support the 44.1/48 kHz family, so
	// the DSP output (see SetInputRate) is resampled to 48 kHz first.
	SampleRate = 48000
	Channels   = 2
	frameBytes = 4 // s16 stereo
)

// Output wraps one long-lived player process fed over stdin.
type Output struct {
	name  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex

	// pending accumulates s16le bytes until a full chunk is ready.
	pending []byte
	// resampler state between WriteAudio calls.
	prevIn float32
	pos    float64 // input-step position of the next output sample
	inRate float64 // DSP-side audio rate feeding the pipe
	step   float64 // input steps per output sample = inRate/SampleRate
	closed bool

	die chan struct{}
}

// Start launches the best available backend: aplay, then mpv. The returned
// output silently no-ops (with a warning) if no backend exists, so the app
// still runs (waterfall only) on dev machines.
func Start() (*Output, error) {
	for _, name := range []string{"aplay", "mpv"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		return startNamed(name, path)
	}
	return nil, fmt.Errorf("no audio backend (aplay/mpv) found")
}

// SetInputRate (re)configures the resampler for the DSP audio rate
// (changes when the capture sample rate changes).
func (o *Output) SetInputRate(rate int) {
	o.mu.Lock()
	o.inRate = float64(rate)
	o.step = float64(rate) / SampleRate
	o.mu.Unlock()
}

func startNamed(name, path string) (*Output, error) {
	var args []string
	switch name {
	case "aplay":
		// ~85 ms buffer: rides out TCP jitter without audible latency.
		args = []string{"-q", "-f", "S16_LE",
			"-r", fmt.Sprint(SampleRate), "-c", fmt.Sprint(Channels),
			"--buffer-size=4096", "--period-size=1024"}
	case "mpv":
		args = []string{"--no-video", "--really-quiet", "--ao=alsa",
			"--demuxer=rawaudio", "--demuxer-rawaudio-format=s16le",
			fmt.Sprintf("--demuxer-rawaudio-rate=%d", SampleRate),
			fmt.Sprintf("--demuxer-rawaudio-channels=%d", Channels)}
	default:
		return nil, fmt.Errorf("unknown backend %q", name)
	}

	o := &Output{name: name, pending: make([]byte, 0, chunkFrames*frameBytes), die: make(chan struct{})}
	o.inRate = float64(dsp.AudioRate)
	o.step = o.inRate / SampleRate
	cmd := exec.Command(path, append(args, "-")...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	o.cmd, o.stdin = cmd, stdin
	go func() {
		err := cmd.Wait()
		fmt.Fprintf(os.Stderr, "audio: %s exited: %v\n", name, err)
		o.mu.Lock()
		o.closed = true
		o.mu.Unlock()
	}()
	return o, nil
}

const chunkFrames = 1024 // ~21 ms of audio

// WriteAudio appends mono float samples at dsp.AudioRate, resamples them
// to SampleRate (linear interpolation is plenty for voice/wideband FM after
// the 15 kHz low-pass) and pushes full chunks into the pipe as they form.
func (o *Output) WriteAudio(mono []float32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	if o.step <= 0 {
		o.inRate = float64(dsp.AudioRate)
		o.step = o.inRate / SampleRate
	}
	for _, s := range mono {
		for o.pos <= 1 {
			out := o.prevIn + (s-o.prevIn)*float32(o.pos)
			v := int16(out * 32767)
			o.pending = append(o.pending, byte(v), byte(v>>8), byte(v), byte(v>>8))
			if len(o.pending) >= chunkFrames*frameBytes {
				o.writePending()
			}
			o.pos += o.step
		}
		o.pos -= 1
		o.prevIn = s
	}
}

// Silence writes ms milliseconds of silence (disconnected state).
func (o *Output) Silence(ms int) {
	frames := SampleRate * ms / 1000
	buf := make([]byte, frames*frameBytes)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.stdin.Write(buf)
}

func (o *Output) writePending() {
	if o.stdin != nil {
		if _, err := o.stdin.Write(o.pending); err != nil {
			o.closed = true
		}
	}
	o.pending = o.pending[:0]
}

// Close terminates the player process.
func (o *Output) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	o.stdin.Close()
	if o.cmd.Process != nil {
		o.cmd.Process.Kill()
	}
}

// WriteWAVMono is a helper for offline tools: writes a 16-bit stereo WAV of
// the mono samples.
func WriteWAVMono(w io.Writer, rate int, mono []float32) error {
	var data []byte
	for _, s := range mono {
		v := int16(s * 32767)
		data = append(data, byte(v), byte(v>>8), byte(v), byte(v>>8))
	}
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+len(data)))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(header[22:24], 2) // stereo
	binary.LittleEndian.PutUint32(header[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(rate*4))
	binary.LittleEndian.PutUint16(header[32:34], 4)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(len(data)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// Pace sleeps so that writing n mono frames would take the right wall time
// at SampleRate — used by offline replay to feed a live pipe.
func Pace(start time.Time, frames int) {
	target := start.Add(time.Duration(frames) * time.Second / SampleRate)
	if d := time.Until(target); d > 0 {
		time.Sleep(d)
	}
}
