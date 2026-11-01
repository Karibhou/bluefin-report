package ui

import (
	"os"
	"strings"
	"testing"
)

func TestDesktopFileMatches(t *testing.T) {
	b, err := os.ReadFile("../../data/" + appID + ".desktop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nIcon=" + appIcon + "\n", "\nName=" + appName + "\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("desktop file lacks %q", want)
		}
	}
}
