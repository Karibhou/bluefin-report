package privacy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/projectbluefin/bluefin-report/internal/snapshot"
)

func str(v string) json.RawMessage { b, _ := json.Marshal(v); return b }

func metric(name, object, value string) snapshot.Metric {
	return snapshot.Metric{Name: name, Object: object, Value: str(value)}
}

func sample() snapshot.Snapshot {
	return snapshot.Snapshot{
		System: snapshot.Report{MediaType: "application/vnd.io.systemd.report", Metrics: []snapshot.Metric{
			metric("io.systemd.Basic.Hostname", "", "orca-desk"),
			metric("io.systemd.Basic.MachineID", "", "0123456789abcdef0123456789abcdef"),
			metric("io.systemd.Basic.BootID", "", "fedcba9876543210fedcba9876543210"),
			metric("io.systemd.Basic.MachineInfo.LOCATION", "", "Jorge's office"),
			metric("io.systemd.Basic.SMBIOS.SystemSerialNumber", "", "PF3ABCD9"),
			metric("io.systemd.Basic.SMBIOS.SystemUUID", "", "4c4c4544-0042-3510-8051-b4c04f4e5a31"),
			metric("io.systemd.Basic.SMBIOS.BaseBoardSerial", "", "L1HF16M00XY"),
			metric("io.systemd.Basic.SMBIOS.ChassisSerialNumber", "", "CHS99887766"),
			metric("io.systemd.Basic.SMBIOS.BaseBoardAssetTag", "", "ASSET-0042"),
			metric("io.systemd.Basic.SMBIOS.ChassisAssetTagNumber", "", "TAG-7788"),
			metric("io.systemd.Basic.SMBIOS.SystemProductName", "", "Laptop 13 (AMD Ryzen 7040Series)"),
			metric("io.systemd.Report.SignPlain.PublicKey", "", "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAuniquekey\n-----END PUBLIC KEY-----"),
			metric("io.systemd.Basic.KernelVersion", "", "7.2.11-ogc4"),
			metric("io.systemd.Basic.OSRelease.IMAGE_ID", "", "dakota-gaming"),
			metric("io.systemd.Files.os-release", "", "ID=bluefin"),
			metric("io.systemd.Manager.UnitActiveState", "var-home-jorge-vault.mount", "failed"),
			metric("io.systemd.Manager.UnitActiveState", "orca-desk-backup.service", "failed"),
			metric("io.systemd.Manager.UnitActiveState", "rpc-statd.service", "failed"),
			metric("io.systemd.Manager.UnitActiveState", "getty@tty1.service", "active"),
			metric("io.systemd.Manager.StateChangeTimestamp", "sshd.service", "00:37:42"),
			{Name: "io.systemd.Network.Address", Object: "wlp1s0", Value: str("192.168.7.21"),
				Fields: map[string]any{"mac": "3c:22:fb:01:aa:9e", "v6": "fe80::3e22:fbff:fe01:aa9e", "n": 1.0}},
			metric("io.systemd.Network.Owner", "", "contact jorge@example.org about /var/home/jorge/notes"),
		}},
		User: snapshot.Report{Metrics: []snapshot.Metric{
			metric("io.systemd.Manager.UnitActiveState", "app-orca\\x2ddesk.scope", "active"),
		}},
	}
}

func TestBuildRemovesIdentifiersAndKeepsDiagnostics(t *testing.T) {
	p, err := Build(sample(), Options{Username: "jorge", IncludeUser: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(p.JSON)
	for _, secret := range []string{"orca-desk", "x2ddesk", "0123456789abcdef", "fedcba98", "Jorge's office", "jorge",
		"192.168.7.21", "3c:22:fb", "fe80::", "example.org", "ID=bluefin",
		"PF3ABCD9", "4c4c4544", "L1HF16M00XY", "CHS99887766", "ASSET-0042", "TAG-7788", "uniquekey"} {
		if strings.Contains(out, secret) {
			t.Errorf("payload still contains %q", secret)
		}
	}
	for _, keep := range []string{"7.2.11-ogc4", "dakota-gaming", "Laptop 13 (AMD Ryzen 7040Series)", "rpc-statd.service", "getty@tty1.service", "00:37:42", "wlp1s0", "var-home-[username]-vault.mount"} {
		if !strings.Contains(out, keep) {
			t.Errorf("payload lost diagnostic value %q", keep)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(p.JSON, &doc); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if doc["user"] == nil {
		t.Error("user scope requested but missing")
	}
	if p.Removed["identifying metric"] != 12 || p.Removed["MAC address"] != 1 || p.Removed["IPv4 address"] != 1 ||
		p.Removed["IPv6 address"] != 1 || p.Removed["email"] != 1 || p.Removed["home path"] != 1 {
		t.Errorf("Removed = %v", p.Removed)
	}
}

// A rejected system report arrives as an empty Report; the hostname must still not leak via user metrics.
func TestBuildScrubsHostnameWithoutSystemReport(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || len(host) < 3 {
		t.Skipf("os.Hostname() = %q, %v: too short for literal scrubbing", host, err)
	}
	s := snapshot.Snapshot{User: snapshot.Report{Metrics: []snapshot.Metric{
		metric("io.systemd.Manager.UnitActiveState", host+"-sync.service", "active"),
		metric("io.systemd.Basic.KernelVersion", "", "running on "+host),
	}}}
	p, err := Build(s, Options{IncludeUser: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(p.JSON), host) {
		t.Errorf("payload leaks os hostname %q:\n%s", host, p.JSON)
	}

	// The metric path still works for a hostname that differs from os.Hostname.
	s.System.Metrics = []snapshot.Metric{metric("io.systemd.Basic.Hostname", "", "zz-synthetic-host")}
	s.User.Metrics = append(s.User.Metrics, metric("io.systemd.Basic.KernelVersion", "", "zz-synthetic-host booted"))
	if p, err = Build(s, Options{IncludeUser: true}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{host, "zz-synthetic-host"} {
		if strings.Contains(string(p.JSON), secret) {
			t.Errorf("payload leaks %q:\n%s", secret, p.JSON)
		}
	}
}

func TestBuildUserScopeAndFilesAreOptIn(t *testing.T) {
	p, err := Build(sample(), Options{Username: "jorge", KeepFiles: []string{"io.systemd.Files.os-release"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(p.JSON), `"user"`) {
		t.Error("user scope attached without opt-in")
	}
	if !strings.Contains(string(p.JSON), "ID=bluefin") {
		t.Error("opted-in file family was dropped")
	}
}

func TestBuildBudgetOmitsWholeFamiliesAndStaysValid(t *testing.T) {
	full, _ := Build(sample(), Options{Username: "jorge"})
	limit := len(full.JSON) - 50

	p, err := Build(sample(), Options{Username: "jorge", MaxBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.JSON) > limit {
		t.Errorf("payload %d bytes exceeds budget %d", len(p.JSON), limit)
	}
	var doc struct {
		System          snapshot.Report
		OmittedFamilies []string
	}
	if err := json.Unmarshal(p.JSON, &doc); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(p.Omitted) == 0 || p.Omitted[0] != "io.systemd.Manager.UnitActiveState" || doc.OmittedFamilies[0] != p.Omitted[0] {
		t.Errorf("Omitted = %v (largest family should go first, and be recorded in the document)", p.Omitted)
	}
	for _, m := range doc.System.Metrics {
		if m.Name == p.Omitted[0] {
			t.Error("omitted family still partially present")
		}
	}

	if _, err := Build(sample(), Options{MaxBytes: 10}); err == nil {
		t.Error("expected error when nothing fits")
	}
}

func TestBuildMasksVirtualInterfaces(t *testing.T) {
	s := snapshot.Snapshot{System: snapshot.Report{Metrics: []snapshot.Metric{
		metric("io.systemd.Network.Address", "tailscale0", "up"),
		metric("io.systemd.Network.Address", "wg0", "up"),
		metric("io.systemd.Network.Address", "docker0", "up"),
		{Name: "io.systemd.Network.Link", Object: "wlp1s0", Value: str("up"), Fields: map[string]any{"peer": "veth1234abc"}},
		metric("io.systemd.Manager.UnitActiveState", "docker0", "active"), // not a network metric
	}}}
	p, err := Build(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(p.JSON)
	for _, name := range []string{"tailscale0", "wg0", "veth1234abc"} {
		if strings.Contains(out, name) {
			t.Errorf("payload still contains %q", name)
		}
	}
	if !strings.Contains(out, "wlp1s0") || p.Removed["virtual interface"] != 4 {
		t.Errorf("Removed = %v, out = %s", p.Removed, out)
	}
}

func TestExtrasAreRedacted(t *testing.T) {
	journal := "Oct 31 10:00:01 orca-desk systemd[1]: jorge-backup failed for jorge from 10.0.0.5\n" +
		"Oct 31 10:00:02 orca-desk kernel: eth0 3c:22:fb:01:aa:9e unit app-orca\\x2ddesk.scope /home/jorge/x"
	p, err := Build(sample(), Options{Username: "jorge", Extras: map[string]string{"bootErrors": journal}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(p.JSON), p.Extras["bootErrors"]} {
		for _, secret := range []string{"orca-desk", "x2ddesk", "jorge", "10.0.0.5", "3c:22:fb"} {
			if strings.Contains(s, secret) {
				t.Errorf("extras still contain %q: %s", secret, s)
			}
		}
	}
	if !strings.Contains(string(p.JSON), `"extras": {`) || !strings.Contains(p.Extras["bootErrors"], "[hostname] kernel") {
		t.Errorf("extras missing from payload: %s", p.Extras["bootErrors"])
	}
	if p, _ := Build(sample(), Options{}); p.Extras != nil || strings.Contains(string(p.JSON), "extras") {
		t.Error("no opt-in must mean no extras")
	}
}

// The artifact is the payload inside a ```json fence; together they must stay under 1 MiB.
func TestDefaultBudgetLeavesRoomForFence(t *testing.T) {
	s := sample()
	for i := range 20000 {
		s.System.Metrics = append(s.System.Metrics, metric("io.systemd.Big", strings.Repeat("x", 40), strings.Repeat("y", i%80)))
	}
	p, err := Build(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len("```json\n") + len(p.JSON) + len("\n```\n"); len(p.Omitted) == 0 || n >= 1<<20 {
		t.Fatalf("fenced artifact is %d bytes, omitted %v", n, p.Omitted)
	}
}
