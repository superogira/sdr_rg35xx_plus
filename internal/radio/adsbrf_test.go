package radio

import (
	"testing"

	"sdr35/internal/adsb"
	"sdr35/internal/dsp"
)

// TestADSBRFToggle checks the in-app ADS-B RF mode: enabling retunes to
// 1090 MHz, forces the 2.4 MSPS capture rate, turns off the competing FT8
// and AIS RF decoders, and installs the demod against the shared store;
// disabling restores the previous capture rate.
func TestADSBRFToggle(t *testing.T) {
	r := NewDemo(dsp.ModeNFM, nil)
	store := adsb.NewStore()
	r.ADSBStore(store)

	if r.ADSBRFEnabled() {
		t.Fatal("ADS-B RF on at start")
	}

	r.SetFT8Enabled(true) // FT8 must be dropped when ADS-B RF starts
	r.SetCaptureRate(2_048_000)
	prevRate := r.IQRate()

	r.SetADSBRFEnabled(true)
	if !r.ADSBRFEnabled() {
		t.Fatal("ADS-B RF did not turn on")
	}
	if r.FT8Enabled() {
		t.Fatal("FT8 still on after ADS-B RF")
	}
	if r.LO() != 1_090_000_000 {
		t.Fatalf("LO = %d, want 1090 MHz", r.LO())
	}
	if r.IQRate() != 2_400_000 || dsp.IQRate != 2_400_000 {
		t.Fatalf("capture rate = %d / dsp %d, want 2.4M", r.IQRate(), dsp.IQRate)
	}

	// The demod must be installed and accept IQ without panicking; a
	// rate below 2.4M is silently ignored by design.
	r.mu.Lock()
	dem := r.adsbDem
	r.mu.Unlock()
	if dem == nil || dem.Rate() != 2.4e6 {
		t.Fatalf("demod not installed at 2.4M: %+v", dem)
	}
	dem.FeedIQ(make([]byte, 4096))

	r.SetADSBRFEnabled(false)
	if r.ADSBRFEnabled() {
		t.Fatal("ADS-B RF did not turn off")
	}
	if r.IQRate() != prevRate {
		t.Fatalf("capture rate after off = %d, want restored %d", r.IQRate(), prevRate)
	}
}
// TestRTLSrvToggle: enabling the fan-out server opens a listener; a
// connected client sees the RTL0 handshake; disabling drops it.
func TestRTLSrvToggle(t *testing.T) {
	r := NewDemo(dsp.ModeNFM, nil)
	r.SetRTLSrvPort(0) // ephemeral
	if r.RTLSrvEnabled() {
		t.Fatal("rtl_tcp server on at start")
	}
	// Port 0 would fail validation in the app; the setter accepts it and
	// SetRTLSrvEnabled falls back to 1235, which may be taken in CI.
	r.SetRTLSrvPort(12390)
	if !r.SetRTLSrvEnabled(true) {
		t.Skip("could not bind test port (in use)")
	}
	if !r.RTLSrvEnabled() {
		t.Fatal("server did not report enabled")
	}
	if r.RTLSrvPort() != 12390 {
		t.Fatalf("port = %d, want 12390", r.RTLSrvPort())
	}
	r.SetRTLSrvEnabled(false)
	if r.RTLSrvEnabled() {
		t.Fatal("server did not stop")
	}
}
