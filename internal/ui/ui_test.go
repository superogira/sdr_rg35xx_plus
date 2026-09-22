package ui

import (
	"fmt"
	"testing"
)

// TestFreqEditorAllCursors renders the editor for every cursor position —
// the slice math once panicked ([:11] with length 10) and killed the app
// the moment the editor opened.
func TestFreqEditorAllCursors(t *testing.T) {
	u := New(640, 480)
	digits := "014550000"
	for c := 0; c <= 8; c++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("cursor %d panicked: %v", c, r)
				}
			}()
			u.DrawFreqEditor(digits, c)
		}()
	}
	fmt.Println("all cursors ok")
}
