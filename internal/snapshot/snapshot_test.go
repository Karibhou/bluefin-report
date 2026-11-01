package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// fake answers systemd-report calls from testdata, keyed by "<scope> <verb>".
type fake struct {
	stdout map[string]string // file name in testdata, or literal output if prefixed with "="
	stderr map[string]string
	err    map[string]error
}

func (f fake) run(_ context.Context, args ...string) ([]byte, []byte, error) {
	key := strings.TrimPrefix(args[0], "--") + " " + args[4]
	var out []byte
	if v, ok := f.stdout[key]; ok {
		if lit, ok := strings.CutPrefix(v, "="); ok {
			out = []byte(lit)
		} else {
			b, err := os.ReadFile("testdata/" + v)
			if err != nil {
				panic(err)
			}
			out = b
		}
	}
	return out, []byte(f.stderr[key]), f.err[key]
}

func realHost() fake {
	return fake{
		stdout: map[string]string{
			"system generate":     "generate-system.json",
			"user generate":       "generate-user.json",
			"system list-sources": "list-sources-system.json",
			"user list-sources":   "list-sources-user.json",
		},
		// Observed on systemd 262.1 as an unprivileged user; exit status was 0.
		stderr: map[string]string{
			"system generate": "Unable to connect to /run/systemd/report/io.systemd.Journal: Permission denied\n",
		},
	}
}

func exitError(t *testing.T) error {
	t.Helper()
	err := exec.Command("false").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	return err
}

func TestTakeSummarisesBothScopesAndReportsUnreachableSources(t *testing.T) {
	s, err := Take(context.Background(), realHost().run, nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := s.Summary()

	if sum.Name != "Bluefin" || sum.ImageID != "dakota-gaming" || sum.ImageVersion != "20261031" ||
		sum.Kernel != "7.2.11-ogc4" || sum.Arch != "x86-64" || sum.SystemState != "degraded" {
		t.Errorf("identity fields wrong: %+v", sum)
	}
	want := []string{"system: nfs-idmapd.service", "system: rpc-statd.service", "user: example-agent.service"}
	if !slices.Equal(sum.FailedUnits, want) {
		t.Errorf("FailedUnits = %v, want %v", sum.FailedUnits, want)
	}
	if len(sum.Unreachable) != 1 || !strings.Contains(sum.Unreachable[0], "io.systemd.Journal") {
		t.Errorf("Unreachable = %v, want the Journal permission failure", sum.Unreachable)
	}
	if len(s.Sources) != 8 || s.Sources[7].Scope != User || s.Sources[0].Scope != System {
		t.Errorf("Sources = %+v", s.Sources)
	}
	// User-scope SystemState must not overwrite the system's.
	if s.User.Metrics[0].String() != "running" {
		t.Errorf("user report not kept separate")
	}
}

func TestTakeNotInstalled(t *testing.T) {
	missing := func(context.Context, ...string) ([]byte, []byte, error) {
		return nil, nil, &fs.PathError{Op: "fork/exec", Path: Binary, Err: fs.ErrNotExist}
	}
	if _, err := Take(context.Background(), missing, nil); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

func TestTakeKeepsOutputWhenExitIsNonZero(t *testing.T) {
	// Hitting METRICS_MAX prints the report, then exits non-zero (EUCLEAN).
	f := realHost()
	f.stderr = map[string]string{"system generate": "Too many metrics, only 4096 metrics collected, 12 metrics skipped.\n"}
	f.err = map[string]error{"system generate": exitError(t)}

	s, err := Take(context.Background(), f.run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Summary().ImageID != "dakota-gaming" {
		t.Error("report discarded on non-zero exit")
	}
	joined := strings.Join(s.Warnings, "\n")
	if !strings.Contains(joined, "4096 metrics collected") || !strings.Contains(joined, "exit status") {
		t.Errorf("truncation not surfaced: %v", s.Warnings)
	}
}

func TestTakeSurvivesBrokenUserScope(t *testing.T) {
	f := realHost()
	f.stdout["user generate"] = "=not json"
	f.err = map[string]error{"user generate": exitError(t)}

	s, err := Take(context.Background(), f.run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Summary().ImageID != "dakota-gaming" || len(s.User.Metrics) != 0 {
		t.Errorf("system report lost or user report invented: %+v", s.User)
	}
	if !strings.Contains(strings.Join(s.Warnings, "\n"), "user generate: unreadable output") {
		t.Errorf("broken user scope not reported: %v", s.Warnings)
	}
}

func TestDescribeReadsJSONSeq(t *testing.T) {
	f := fake{stdout: map[string]string{"system describe": "describe-system.seq"}}
	fams, warn, err := Describe(context.Background(), f.run, System)
	if err != nil || len(warn) != 0 {
		t.Fatalf("err=%v warn=%v", err, warn)
	}
	if len(fams) != 2 || fams["io.systemd.Basic.CPUUsage"].Type != "counter" {
		t.Errorf("fams = %+v", fams)
	}
}

func TestTakeCancelledReturnsErrorNotPartialSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := realHost()
	killed := func(c context.Context, args ...string) ([]byte, []byte, error) {
		out, stderr, _ := f.run(c, args...)
		cancel() // as if the user hit Refresh again while systemd-report was running
		return out[:len(out)/2], stderr, exitError(t)
	}
	if _, err := Take(ctx, killed, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
