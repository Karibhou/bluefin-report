// Command bluefin-report is the Bluefin Report app. --print-summary prints
// the Overview data without a GUI; --dry-run never calls the real gh.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/projectbluefin/bluefin-report/internal/snapshot"
	"github.com/projectbluefin/bluefin-report/internal/ui"
)

func main() {
	printSummary := flag.Bool("print-summary", false, "collect a snapshot and print the overview")
	dryRun := flag.Bool("dry-run", false, "log gh calls instead of publishing")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *version {
		fmt.Println(ui.Version)
		return
	}
	if !*printSummary {
		os.Exit(ui.Run(*dryRun))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := snapshot.Take(ctx, snapshot.Run, nil)
	if errors.Is(err, snapshot.ErrNotInstalled) {
		fmt.Fprintf(os.Stderr, "%s not found: this system needs systemd 262\n", snapshot.Binary)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	sum := s.Summary()
	fmt.Printf("System:   %s %s (%s)\n", sum.Name, sum.ImageVersion, sum.ImageID)
	fmt.Printf("Kernel:   %s %s\n", sum.Kernel, sum.Arch)
	fmt.Printf("State:    %s\n", sum.SystemState)
	fmt.Printf("Metrics:  %d system, %d user, from %d sources\n", len(s.System.Metrics), len(s.User.Metrics), len(s.Sources))
	fmt.Printf("Failed:   %d\n", len(sum.FailedUnits))
	for _, u := range sum.FailedUnits {
		fmt.Println("  " + u)
	}
	if len(sum.Warnings) > 0 {
		fmt.Printf("Warnings: %d\n  %s\n", len(sum.Warnings), strings.Join(sum.Warnings, "\n  "))
	}
}
