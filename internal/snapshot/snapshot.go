// Package snapshot collects a point-in-time report from systemd-report (v262).
//
// It never fails because one source is unreachable or one scope errors:
// those become Warnings. It fails only when systemd-report cannot run at all.
package snapshot

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Binary is where systemd 262 installs systemd-report; it is not on $PATH.
// BLUEFIN_REPORT_SYSTEMD_REPORT overrides it (testing the missing-binary page).
var Binary = cmp.Or(os.Getenv("BLUEFIN_REPORT_SYSTEMD_REPORT"), "/usr/lib/systemd/systemd-report")

// Exec runs systemd-report with args and returns its stdout and stderr.
// err is non-nil when the process could not start or exited non-zero.
type Exec func(ctx context.Context, args ...string) (stdout, stderr []byte, err error)

// Run is the real Exec. Output is capped so a misbehaving source cannot exhaust memory.
func Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, Binary, args...)
	var out, errb capped
	out.max, errb.max = 8<<20, 1<<20
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if out.over {
		err = errors.Join(err, errors.New("systemd-report output exceeded 8 MiB and was truncated"))
	}
	return out.Bytes(), errb.Bytes(), err
}

type capped struct {
	bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); len(p) > room {
		c.over = true
		if room > 0 {
			c.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

// Scope is the systemd-report runtime scope.
type Scope string

const (
	System Scope = "system"
	User   Scope = "user"
)

// Metric is one entry of a systemd-report report. Value and Fields are kept raw:
// their types vary per family and privacy redaction needs the original values.
type Metric struct {
	Name   string          `json:"name"`
	Object string          `json:"object,omitempty"`
	Fields map[string]any  `json:"fields,omitempty"`
	Value  json.RawMessage `json:"value"`
}

// String returns Value as a string if it is a JSON string, else its JSON text.
func (m Metric) String() string {
	var s string
	if json.Unmarshal(m.Value, &s) == nil {
		return s
	}
	return string(m.Value)
}

// Report is the object emitted by `systemd-report generate`.
type Report struct {
	MediaType string   `json:"mediaType"`
	Timestamp string   `json:"timestamp"`
	Metrics   []Metric `json:"metrics"`
}

// Source is one entry of `systemd-report list-sources`.
type Source struct {
	Scope   Scope  `json:"-"`
	Name    string `json:"source"`
	Address string `json:"address"`
}

// Snapshot is immutable once returned. System and User are separate reports:
// both scopes publish io.systemd.Manager.* and merging would lose provenance.
type Snapshot struct {
	TakenAt  time.Time
	System   Report
	User     Report
	Sources  []Source
	Warnings []string
}

// ErrNotInstalled means the systemd-report executable could not be found.
var ErrNotInstalled = errors.New("systemd-report is not installed")

// Take collects both scopes. matches limits metric families (empty = everything).
// A cancelled ctx returns ctx.Err(), never a partial snapshot.
func Take(ctx context.Context, run Exec, matches []string) (Snapshot, error) {
	s := Snapshot{TakenAt: time.Now()}
	for _, scope := range []Scope{System, User} {
		rep, err := generate(ctx, run, scope, matches, &s.Warnings)
		if err != nil {
			return Snapshot{}, err
		}
		if scope == System {
			s.System = rep
		} else {
			s.User = rep
		}
		src, err := listSources(ctx, run, scope, &s.Warnings)
		if err != nil {
			return Snapshot{}, err
		}
		s.Sources = append(s.Sources, src...)
	}
	return s, nil
}

func baseArgs(scope Scope) []string {
	return []string{"--" + string(scope), "--no-pager", "--no-legend", "--json=short"}
}

// call runs one verb, records stderr lines as warnings and returns startup failures.
// A non-zero exit is not fatal: systemd-report exits non-zero after printing a usable
// result when caps are hit, so callers decide based on whether stdout parses.
func call(ctx context.Context, run Exec, scope Scope, verb string, extra []string, warn *[]string) ([]byte, error) {
	args := append(baseArgs(scope), verb)
	args = append(args, extra...)
	out, stderr, err := run(ctx, args...)
	// Checked first: cancellation kills the child, which surfaces as an ExitError.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotInstalled
		}
		var pathErr *fs.PathError
		var execErr *exec.Error
		if errors.As(err, &pathErr) || errors.As(err, &execErr) {
			return nil, fmt.Errorf("%s %s: cannot start systemd-report: %w", scope, verb, err)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(stderr)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			*warn = append(*warn, fmt.Sprintf("%s %s: %s", scope, verb, line))
		}
	}
	if err != nil {
		*warn = append(*warn, fmt.Sprintf("%s %s: %v", scope, verb, err))
	}
	return out, nil
}

func generate(ctx context.Context, run Exec, scope Scope, matches []string, warn *[]string) (Report, error) {
	out, err := call(ctx, run, scope, "generate", matches, warn)
	if err != nil {
		return Report{}, err
	}
	var r Report
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&r); err != nil {
		*warn = append(*warn, fmt.Sprintf("%s generate: unreadable output: %v", scope, err))
		return Report{}, nil
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		*warn = append(*warn, fmt.Sprintf("%s generate: unreadable output: expected a single JSON document", scope))
		return Report{}, nil
	}
	if r.MediaType != "application/vnd.io.systemd.report" || strings.TrimSpace(r.Timestamp) == "" || r.Metrics == nil {
		*warn = append(*warn, fmt.Sprintf("%s generate: unreadable output: invalid report envelope (expected mediaType, timestamp and metrics array)", scope))
		return Report{}, nil
	}
	return r, nil
}

func listSources(ctx context.Context, run Exec, scope Scope, warn *[]string) ([]Source, error) {
	out, err := call(ctx, run, scope, "list-sources", nil, warn)
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil, err
	}
	var src []Source
	if err := json.Unmarshal(out, &src); err != nil {
		*warn = append(*warn, fmt.Sprintf("%s list-sources: unreadable output: %v", scope, err))
		return nil, nil
	}
	for i := range src {
		src[i].Scope = scope
	}
	return src, nil
}

// Family is one entry of `systemd-report describe`.
type Family struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// Describe returns metric family metadata for a scope, keyed by family name.
// Separate from Take because the Overview does not need it.
func Describe(ctx context.Context, run Exec, scope Scope) (map[string]Family, []string, error) {
	var warn []string
	out, err := call(ctx, run, scope, "describe", nil, &warn)
	if err != nil {
		return nil, warn, err
	}
	fams := map[string]Family{}
	err = readSeq(out, func(raw []byte) error {
		var f Family
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		fams[f.Name] = f
		return nil
	})
	if err != nil {
		warn = append(warn, fmt.Sprintf("%s describe: unreadable output: %v", scope, err))
	}
	return fams, warn, nil
}

// readSeq splits RFC 7464 JSON text sequences (RS-prefixed records), which is what
// systemd-report metrics/describe emit with --json.
func readSeq(b []byte, each func([]byte) error) error {
	for _, rec := range bytes.Split(b, []byte{0x1e}) {
		if rec = bytes.TrimSpace(rec); len(rec) == 0 {
			continue
		}
		if err := each(rec); err != nil {
			return err
		}
	}
	return nil
}

// Summary is what the Overview page shows.
type Summary struct {
	Name, ImageID, ImageVersion, Kernel, Arch, SystemState string
	FailedUnits                                            []string // "system: foo.service"
	Unreachable                                            []string // warnings mentioning a source connection failure
	Warnings                                               []string
}

// Summary derives the Overview from the snapshot.
func (s Snapshot) Summary() Summary {
	sum := Summary{Warnings: s.Warnings}
	for _, m := range s.System.Metrics {
		switch m.Name {
		case "io.systemd.Basic.OSRelease.NAME":
			sum.Name = m.String()
		case "io.systemd.Basic.OSRelease.IMAGE_ID":
			sum.ImageID = m.String()
		case "io.systemd.Basic.OSRelease.IMAGE_VERSION":
			sum.ImageVersion = m.String()
		case "io.systemd.Basic.KernelVersion":
			sum.Kernel = m.String()
		case "io.systemd.Basic.Architecture":
			sum.Arch = m.String()
		case "io.systemd.Manager.SystemState":
			sum.SystemState = m.String()
		}
	}
	for scope, rep := range map[Scope]Report{System: s.System, User: s.User} {
		for _, m := range rep.Metrics {
			if m.Name == "io.systemd.Manager.UnitActiveState" && m.String() == "failed" {
				sum.FailedUnits = append(sum.FailedUnits, string(scope)+": "+m.Object)
			}
		}
	}
	sort.Strings(sum.FailedUnits)
	for _, w := range s.Warnings {
		if strings.Contains(w, "Unable to connect to") {
			sum.Unreachable = append(sum.Unreachable, w)
		}
	}
	return sum
}
