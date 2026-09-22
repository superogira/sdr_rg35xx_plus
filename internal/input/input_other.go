//go:build !linux

// Package input: on non-Linux dev machines there is no evdev gamepad; the
// app runs without button input (screenshot/CI testing).
package input

import "fmt"

type Button int

const (
	Up Button = iota
	Down
	Left
	Right
	A
	B
	X
	Y
	L1
	R1
	L2
	R2
	Select
	Start
	Menu
	VolDown
	VolUp
)

func (b Button) String() string { return fmt.Sprintf("BTN(%d)", int(b)) }

type Event struct {
	Button Button
	Down   bool
}

type Reader struct{}

func Open() (*Reader, error) { return nil, fmt.Errorf("evdev input requires Linux") }

func (r *Reader) Poll()          {}
func (r *Reader) Events() []Event { return nil }
func (r *Reader) Close()         {}
