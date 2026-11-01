package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWindowState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	def := windowState{Width: 720, Height: 760}
	if got := loadWindowState(); got != def {
		t.Fatalf("missing file: %+v", got)
	}
	os.MkdirAll(filepath.Dir(windowFile()), 0o700)
	for content, want := range map[string]windowState{
		`{"Width":`:                      def,
		`{"Width":10,"Height":10}`:       {Width: 360, Height: 294},
		`{"Width":900,"Maximized":true}`: {Width: 900, Height: 760, Maximized: true},
		`{"Width":1000,"Height":800}`:    {Width: 1000, Height: 800},
	} {
		os.WriteFile(windowFile(), []byte(content), 0o600)
		if got := loadWindowState(); got != want {
			t.Errorf("%s: got %+v, want %+v", content, got, want)
		}
	}
}
