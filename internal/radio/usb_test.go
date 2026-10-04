package radio

import (
	"testing"

	"sdr35/internal/dsp"
)

func TestUSBSourceSelection(t *testing.T) {
	r := New(USBHost, 96_500_000, dsp.ModeNFM, 20, nil)
	if !r.USBSource() {
		t.Fatalf("New(host=usb) should select the USB source")
	}
	if got := r.Hostname(); got != "USB" {
		t.Fatalf("Hostname() = %q, want \"USB\"", got)
	}
	// Switching to a TCP host must clear the flag...
	r.SetHost("192.168.2.151:1234")
	if r.USBSource() {
		t.Fatalf("USBSource should be false after SetHost(tcp)")
	}
	if got := r.Hostname(); got != "192.168.2.151:1234" {
		t.Fatalf("Hostname() = %q, want the tcp host", got)
	}
	// ...and selecting USB again must set it without touching the stored
	// host string (the dial layer maps it to 127.0.0.1:1234).
	r.SetHost(USBHost)
	if !r.USBSource() || r.Hostname() != "USB" {
		t.Fatalf("SetHost(usb) should re-select the USB source (got %q)", r.Hostname())
	}
	// Disabling the radio keeps USB off.
	r.SetHost("")
	if r.USBSource() {
		t.Fatalf("USBSource should be false after disable")
	}
}
