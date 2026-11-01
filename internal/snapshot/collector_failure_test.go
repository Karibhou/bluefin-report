package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestCollectorStartupFailuresAreFatal(t *testing.T) {
	for _, failure := range []error{
		&fs.PathError{Op: "fork/exec", Path: Binary, Err: fs.ErrPermission},
		&fs.PathError{Op: "fork/exec", Path: Binary, Err: syscall.ENOEXEC},
		&exec.Error{Name: Binary, Err: fs.ErrPermission},
	} {
		for _, key := range []string{"system generate", "system list-sources", "user generate", "user list-sources"} {
			f := realHost()
			f.err = map[string]error{key: failure}
			s, err := Take(context.Background(), f.run, nil)
			if !errors.Is(err, failure) || errors.Is(err, ErrNotInstalled) || !s.TakenAt.IsZero() {
				t.Fatalf("%s: snapshot=%+v err=%v, want fatal %v without partial snapshot", key, s, err, failure)
			}
		}
		f := fake{err: map[string]error{"system describe": failure}}
		if _, _, err := Describe(context.Background(), f.run, System); !errors.Is(err, failure) {
			t.Fatalf("describe: err=%v, want %v", err, failure)
		}
	}
}

func TestCollectorRejectsInvalidReportWithoutLosingOtherScope(t *testing.T) {
	const envelope = `{"mediaType":"application/vnd.io.systemd.report","metrics":[]}`
	for _, output := range []string{
		"", " \n\t", "null", "{}",
		`{"mediaType":"wrong","metrics":[]}`,
		`{"mediaType":"application/vnd.io.systemd.report"}`,
		`{"mediaType":"application/vnd.io.systemd.report","metrics":null}`,
		`{"mediaType":"application/vnd.io.systemd.report","metrics":{}}`,
		envelope + " null", envelope + " {}", envelope + " junk",
	} {
		for _, scope := range []string{"system", "user"} {
			f := realHost()
			f.stdout[scope+" generate"] = "=" + output
			s, err := Take(context.Background(), f.run, nil)
			if err != nil {
				t.Fatal(err)
			}
			broken, healthy := s.System, s.User
			if scope == "user" {
				broken, healthy = s.User, s.System
			}
			if len(broken.Metrics) != 0 || len(healthy.Metrics) == 0 || !strings.Contains(strings.Join(s.Warnings, "\n"), scope+" generate: unreadable output") {
				t.Fatalf("%s output %q: snapshot=%+v", scope, output, s)
			}
		}
	}
	f := realHost()
	f.stdout["system generate"] = "=" + envelope
	f.stderr = nil
	s, err := Take(context.Background(), f.run, nil)
	if err != nil || len(s.Warnings) != 0 || s.System.Metrics == nil {
		t.Fatalf("valid empty report: snapshot=%+v err=%v", s, err)
	}
}

func TestCollectorPreservesNestedFieldNumbers(t *testing.T) {
	f := realHost()
	f.stdout["system generate"] = `={"mediaType":"application/vnd.io.systemd.report","metrics":[{"name":"counter","value":9007199254740993,"fields":{"nested":{"count":9007199254740993},"samples":[18446744073709551615,1.234567890123456789]}}]}`
	s, err := Take(context.Background(), f.run, nil)
	if err != nil || len(s.System.Metrics) != 1 {
		t.Fatalf("snapshot=%+v err=%v", s, err)
	}
	m := s.System.Metrics[0]
	fields := m.Fields
	if fields["nested"].(map[string]any)["count"] != json.Number("9007199254740993") ||
		fields["samples"].([]any)[0] != json.Number("18446744073709551615") ||
		fields["samples"].([]any)[1] != json.Number("1.234567890123456789") || m.String() != "9007199254740993" {
		t.Fatalf("numeric precision lost: %+v", m)
	}
}
