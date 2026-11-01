package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const emptyReportJSON = `{"mediaType":"application/vnd.io.systemd.report","timestamp":"Sun 2026-11-01 00:37:49 UTC","metrics":[]}`

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
	for _, missingErr := range []error{
		&fs.PathError{Op: "fork/exec", Path: Binary, Err: fs.ErrNotExist},
		&exec.Error{Name: Binary, Err: exec.ErrNotFound},
	} {
		missing := func(context.Context, ...string) ([]byte, []byte, error) {
			return nil, nil, missingErr
		}
		if _, err := Take(context.Background(), missing, nil); !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("err = %v, want ErrNotInstalled for %v", err, missingErr)
		}
	}
}

func TestTakeCannotStartBinary(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode fs.FileMode
		want error
	}{
		{name: "permission denied", mode: 0600, want: fs.ErrPermission},
		{name: "invalid executable format", mode: 0700, want: syscall.ENOEXEC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldBinary := Binary
			Binary = filepath.Join(t.TempDir(), "systemd-report")
			t.Cleanup(func() { Binary = oldBinary })
			if err := os.WriteFile(Binary, []byte("not an executable\n"), tc.mode); err != nil {
				t.Fatal(err)
			}

			s, err := Take(context.Background(), Run, nil)
			if !errors.Is(err, tc.want) || errors.Is(err, ErrNotInstalled) {
				t.Fatalf("err = %v, want fatal %v (not ErrNotInstalled)", err, tc.want)
			}
			if !s.TakenAt.IsZero() {
				t.Fatalf("startup failure returned a successful or partial snapshot: %+v", s)
			}
		})
	}
}

func TestTakeStartupFailureInAnyCallIsFatal(t *testing.T) {
	for _, key := range []string{"system generate", "system list-sources", "user generate", "user list-sources"} {
		t.Run(key, func(t *testing.T) {
			f := realHost()
			f.err = map[string]error{
				key: &fs.PathError{Op: "fork/exec", Path: Binary, Err: fs.ErrPermission},
			}
			s, err := Take(context.Background(), f.run, nil)
			if !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("err = %v, want fatal permission error", err)
			}
			if !s.TakenAt.IsZero() {
				t.Fatalf("startup failure returned a partial snapshot: %+v", s)
			}
		})
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

func TestTakeWarnsForMissingOrInvalidGenerateEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
	}{
		{name: "blank", output: ""},
		{name: "whitespace", output: " \n\t"},
		{name: "null", output: "null"},
		{name: "empty object", output: "{}"},
		{name: "missing media type", output: `{"timestamp":"now","metrics":[]}`},
		{name: "wrong media type", output: `{"mediaType":"text/plain","timestamp":"now","metrics":[]}`},
		{name: "missing timestamp", output: `{"mediaType":"application/vnd.io.systemd.report","metrics":[]}`},
		{name: "blank timestamp", output: `{"mediaType":"application/vnd.io.systemd.report","timestamp":" ","metrics":[]}`},
		{name: "missing metrics", output: `{"mediaType":"application/vnd.io.systemd.report","timestamp":"now"}`},
		{name: "null metrics", output: `{"mediaType":"application/vnd.io.systemd.report","timestamp":"now","metrics":null}`},
		{name: "invalid metrics", output: `{"mediaType":"application/vnd.io.systemd.report","timestamp":"now","metrics":{}}`},
		{name: "invalid JSON", output: "not json"},
		{name: "array", output: "[]"},
		{name: "trailing document", output: emptyReportJSON + "\nnull"},
		{name: "trailing junk", output: emptyReportJSON + "\njunk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, scope := range []Scope{System, User} {
				t.Run(string(scope), func(t *testing.T) {
					f := realHost()
					f.stderr = nil
					f.stdout[string(scope)+" generate"] = "=" + tc.output
					s, err := Take(context.Background(), f.run, nil)
					if err != nil {
						t.Fatal(err)
					}
					broken := s.System
					if scope == User {
						broken = s.User
						if s.Summary().ImageID != "dakota-gaming" {
							t.Error("healthy system scope lost")
						}
					} else if len(s.User.Metrics) == 0 || s.User.Metrics[0].String() != "running" {
						t.Error("healthy user scope lost")
					}
					if broken.MediaType != "" || broken.Timestamp != "" || broken.Metrics != nil {
						t.Errorf("invalid scope retained a partial report: %+v", broken)
					}
					if len(s.Warnings) != 1 || !strings.HasPrefix(s.Warnings[0], string(scope)+" generate: unreadable output:") {
						t.Errorf("missing scoped warning: %v", s.Warnings)
					}
					if len(s.Summary().Warnings) != 1 {
						t.Error("invalid output appears healthy in the overview")
					}
				})
			}
		})
	}
}

func TestTakeAcceptsEmptyMetricsReport(t *testing.T) {
	f := realHost()
	f.stderr = nil
	f.stdout["system generate"] = "=" + emptyReportJSON + " \n\t"
	f.stdout["user generate"] = "=" + emptyReportJSON
	s, err := Take(context.Background(), f.run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("valid empty report produced warnings: %v", s.Warnings)
	}
	for _, report := range []Report{s.System, s.User} {
		if report.MediaType != "application/vnd.io.systemd.report" || report.Timestamp == "" || report.Metrics == nil || len(report.Metrics) != 0 {
			t.Errorf("valid empty report was discarded: %+v", report)
		}
	}
}

func TestTakePreservesNumericFieldsThroughMarshal(t *testing.T) {
	f := realHost()
	f.stderr = nil
	f.stdout["system generate"] = `={"mediaType":"application/vnd.io.systemd.report","timestamp":"now","metrics":[{"name":"io.systemd.Test.Counter","fields":{"count":18446744073709551615,"nested":{"count":9007199254740993},"array":[9007199254740995]},"value":18446744073709551615}]}`
	s, err := Take(context.Background(), f.run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 0 || len(s.System.Metrics) != 1 {
		t.Fatalf("numeric report not collected: warnings=%v report=%+v", s.Warnings, s.System)
	}
	m := s.System.Metrics[0]
	if count, ok := m.Fields["count"].(json.Number); !ok || count.String() != "18446744073709551615" {
		t.Errorf("numeric field rounded or converted: %#v", m.Fields["count"])
	}
	b, err := json.Marshal(s.System)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{`"count":18446744073709551615`, `"count":9007199254740993`, `"array":[9007199254740995]`, `"value":18446744073709551615`} {
		if !strings.Contains(string(b), token) {
			t.Errorf("marshal lost original numeric token %s: %s", token, b)
		}
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

func TestDescribeCannotStartBinary(t *testing.T) {
	f := fake{err: map[string]error{
		"system describe": &exec.Error{Name: Binary, Err: fs.ErrPermission},
	}}
	fams, warn, err := Describe(context.Background(), f.run, System)
	if !errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want fatal permission error", err)
	}
	if fams != nil || len(warn) != 0 {
		t.Fatalf("startup failure returned partial metadata: families=%v warnings=%v", fams, warn)
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
