package privacy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/projectbluefin/bluefin-report/internal/snapshot"
)

func TestBuildResolvesHostIdentityWithoutSystemMetrics(t *testing.T) {
	original := hostname
	t.Cleanup(func() { hostname = original })
	for _, host := range []string{"q", "qx", "private-host"} {
		t.Run(host, func(t *testing.T) {
			hostname = func() (string, error) { return host, nil }
			input := host + " zx app-" + strings.ReplaceAll(host, "-", `\x2d`) + ".scope"
			m := metric("io.systemd.Journal.Message", input, input)
			m.Fields = map[string]any{"message": input}
			s := snapshot.Snapshot{User: snapshot.Report{Metrics: []snapshot.Metric{m}}}
			p, err := Build(s, Options{Username: "zx", IncludeUser: true, Extras: map[string]string{"bootErrors": input}})
			if err != nil {
				t.Fatal(err)
			}
			want := "[hostname] [username] app-[hostname].scope"
			if p.Extras["bootErrors"] != want || strings.Count(string(p.JSON), want) != 4 {
				t.Fatalf("identity leaked or diagnostics changed: %s", p.JSON)
			}
		})
	}
}

func TestBuildCollectsReportedIdentitiesAndShortUsernames(t *testing.T) {
	original := hostname
	t.Cleanup(func() { hostname = original })
	hostname = func() (string, error) { return "actual-host", nil }
	for _, username := range []string{"q", "qx"} {
		s := snapshot.Snapshot{
			System: snapshot.Report{Metrics: []snapshot.Metric{metric("io.systemd.Basic.Hostname", "", "old-host")}},
			User:   snapshot.Report{Metrics: []snapshot.Metric{metric("io.systemd.Basic.Hostname", "", "user-host")}},
		}
		input := "actual-host old-host user-host " + username + " " + username + "backup.service"
		p, err := Build(s, Options{Username: username, IncludeUser: true, Extras: map[string]string{"bootErrors": input}})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := p.Extras["bootErrors"], "[hostname] [hostname] [hostname] [username] "+username+"backup.service"; got != want {
			t.Fatalf("extras = %q, want %q", got, want)
		}
	}
}

func TestBuildFailsClosedWithoutHostnameIdentity(t *testing.T) {
	original := hostname
	t.Cleanup(func() { hostname = original })
	for _, lookup := range []func() (string, error){
		func() (string, error) { return "", errors.New("hostname unavailable") },
		func() (string, error) { return "", nil },
	} {
		hostname = lookup
		p, err := Build(snapshot.Snapshot{}, Options{Extras: map[string]string{"bootErrors": "private log"}})
		if err == nil || p.JSON != nil || p.Extras != nil {
			t.Fatalf("unresolved identity returned a publishable payload: %+v, %v", p, err)
		}
		if _, err := Build(sample(), Options{}); err != nil {
			t.Fatalf("reported hostname should provide fallback identity: %v", err)
		}
	}
}

func TestBuildScrubsNestedIdentityWithoutChangingNumbersOrSnapshot(t *testing.T) {
	original := hostname
	t.Cleanup(func() { hostname = original })
	hostname = func() (string, error) { return "private-host", nil }
	const raw = `{"nested":["private-host",{"login":"qx","n":9007199254740993,"f":1.2300e+04}]}`
	const untouched = `[ "diagnostic", 9007199254740993, 1.2300e+04 ]`
	fields := map[string]any{"nested": []any{"private-host", map[string]any{"login": "qx"}}}
	s := snapshot.Snapshot{User: snapshot.Report{Metrics: []snapshot.Metric{
		{Name: "io.systemd.Journal.Message", Fields: fields, Value: json.RawMessage(raw)},
		{Name: "io.systemd.Journal.Other", Value: json.RawMessage(untouched)},
	}}}
	p, err := Build(s, Options{IncludeUser: true, Username: "qx"})
	if err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(p.JSON, &doc); err != nil {
		t.Fatal(err)
	}
	out := string(p.JSON)
	if strings.Contains(out, "private-host") || strings.Contains(out, "qx") ||
		!strings.Contains(out, "9007199254740993") || !strings.Contains(out, "1.2300e+04") {
		t.Fatalf("nested identity leaked or numeric precision changed: %s", out)
	}
	if doc.User.Metrics[0].Fields["nested"].([]any)[0] != "[hostname]" {
		t.Fatalf("nested field was not redacted: %s", out)
	}
	if string(s.User.Metrics[0].Value) != raw || fields["nested"].([]any)[0] != "private-host" ||
		fields["nested"].([]any)[1].(map[string]any)["login"] != "qx" {
		t.Fatal("Build mutated the source snapshot")
	}
	r := redactor{removed: map[string]int{}}
	r.literal("hostname", "private-host")
	if got := r.report(s.User, nil).Metrics[1].Value; string(got) != untouched {
		t.Fatalf("untouched metric encoding changed: %s", got)
	}
}
