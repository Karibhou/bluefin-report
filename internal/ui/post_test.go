package ui

import (
	"testing"

	"codeberg.org/puregotk/puregotk/v4/glib"
)

// 5000 posts exceed purego's 2000-slot callback table; this only passes
// because every post reuses the one &mainCb slot. Needs GLib, not a display.
func TestPostReusesCallback(t *testing.T) {
	ctx := glib.MainContextDefault()
	n := 0
	for range 5000 {
		post(func() { n++ })
		for ctx.Iteration(false) {
		}
	}
	if n != 5000 {
		t.Fatalf("ran %d of 5000 posts", n)
	}
}
