package radio

import (
	"testing"

	"sdr35/internal/dsp"
)

// TestFT8ModeLock: enabling FT8 must switch the demod to USB, and while
// FT8 runs every SetMode to another mode must be rejected (re-setting
// the same mode stays allowed — internal chain rebuilds depend on it).
func TestFT8ModeLock(t *testing.T) {
	r := NewDemo(dsp.ModeAM, nil)

	if r.Mode().Name != "AM" {
		t.Fatalf("start mode = %s, want AM", r.Mode().Name)
	}

	r.SetFT8Enabled(true)
	if r.Mode().Name != dsp.ModeUSB.Name {
		t.Fatalf("after FT8 on: mode = %s, want USB", r.Mode().Name)
	}

	for _, m := range []dsp.Mode{dsp.ModeNFM, dsp.ModeWFM, dsp.ModeLSB, dsp.ModeCW, dsp.ModeAM} {
		r.SetMode(m)
		if r.Mode().Name != dsp.ModeUSB.Name {
			t.Fatalf("SetMode(%s) while FT8 on switched to %s — lock broken", m.Name, r.Mode().Name)
		}
	}

	// Same-mode re-set (chain rebuild path) must not be blocked.
	r.SetMode(dsp.ModeUSB)
	if r.Mode().Name != dsp.ModeUSB.Name {
		t.Fatalf("same-mode re-set rejected")
	}

	// Turning FT8 off releases the lock again.
	r.SetFT8Enabled(false)
	r.SetMode(dsp.ModeNFM)
	if r.Mode().Name != dsp.ModeNFM.Name {
		t.Fatalf("after FT8 off: SetMode(NFM) gave %s — still locked", r.Mode().Name)
	}
}
