package extras

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const id = "22791cb751e37b3ee143a287d1922f27"

func fixture(t *testing.T) {
	t.Helper()
	Root = t.TempDir()
	t.Cleanup(func() { Root = "/" })
	for p, c := range map[string]string{
		"proc/cmdline": `initrd=\EFI\Linux\bootc_composefs-ab\initrd root=LABEL=root rw quiet composefs.digest=v1-sha512-12:` + id + "\n",
		"proc/cpuinfo": "processor\t: 0\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD Ryzen 5 7600X 6-Core Processor\n",
		"sysroot/state/deploy/" + id + "/" + id + ".origin": "[origin]\ncontainer-image-reference = docker://ghcr.io/projectbluefin/dakota:testing\n\n" +
			"[boot]\ndigest = ae77\n\n[image]\nmanifest_digest = sha256:4a06d9\n",
		"sysroot/state/deploy/other/other.origin": "[image]\nmanifest_digest = sha256:wrong\n",
		"sys/class/drm/card0/device/uevent":       "DRIVER=amdgpu\nPCI_CLASS=30000\nPCI_ID=1002:164E\n",
		"sys/class/drm/card1/device/uevent":       "DRIVER=nvidia\nPCI_ID=10de:2684\n",
		"sys/class/drm/card0-DP-1/device/uevent":  "DRIVER=amdgpu\nPCI_ID=1002:164E\n",
		"sys/class/drm/renderD128/device/uevent":  "PCI_ID=1002:164E\n",
	} {
		p = filepath.Join(Root, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func stub(t *testing.T, out map[string]string) *[]string {
	var calls []string
	old := run
	run = func(_ context.Context, name string, args ...string) string {
		k := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, k)
		return out[k]
	}
	t.Cleanup(func() { run = old })
	return &calls
}

func TestProbe(t *testing.T) {
	fixture(t)
	stub(t, map[string]string{
		"/usr/libexec/ublue-booted-image ref": "ghcr.io/projectbluefin/dakota:testing",
		"/usr/libexec/ublue-booted-image tag": "testing",
		"gnome-shell --version":               "GNOME Shell 51.1",
	})
	want := Identity{Ref: "ghcr.io/projectbluefin/dakota:testing", Tag: "testing", Digest: "sha256:4a06d9",
		CPU: "AMD Ryzen 5 7600X 6-Core Processor", GPU: "AMD 1002:164E (amdgpu), NVIDIA 10DE:2684 (nvidia)", GNOME: "51.1"}
	if got := Probe(context.Background()); got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestProbeToleratesMissingSources(t *testing.T) {
	Root = t.TempDir()
	t.Cleanup(func() { Root = "/" })
	stub(t, nil)
	if got := Probe(context.Background()); got != (Identity{}) {
		t.Fatalf("got %+v", got)
	}
}

func TestCollectOnlyOptedIn(t *testing.T) {
	calls := stub(t, map[string]string{"coredumpctl list --no-pager --since -7d": "TIME PID\nx 1 /usr/bin/foo"})
	if got := Collect(context.Background(), nil); len(got) != 0 || len(*calls) != 0 {
		t.Fatalf("nothing opted in, yet collected %v via %v", got, *calls)
	}
	got := Collect(context.Background(), []string{"crashes", "flatpaks"})
	if len(*calls) != 2 || got["crashes"] == "" || len(got) != 2 {
		t.Fatalf("%v %v", got, *calls)
	}
	md := Markdown(got)
	if !strings.Contains(md, "<details><summary>Crashes in the Last 7 Days</summary>") || strings.Contains(md, "Flatpak") {
		t.Fatalf("markdown: %s", md)
	}
}

// Diagnostic output lands verbatim inside a code fence in a public issue body, so a
// line of backticks in it must not close the fence, and an opted-in section that
// printed nothing must still say so instead of rendering an empty block.
func TestMarkdownEscapesFencesAndMarksEmpty(t *testing.T) {
	md := Markdown(map[string]string{
		"crashes":    "a\n```\n# injected heading\n```",
		"bootErrors": "",
		"flatpaks":   "org.example.App 1.0",
	})
	want := "\n\n<details><summary>Errors From This Boot</summary>\n\n```\n(none)\n```\n\n</details>" +
		"\n\n<details><summary>Crashes in the Last 7 Days</summary>\n\n```\na\n'''\n# injected heading\n'''\n```\n\n</details>"
	if md != want {
		t.Fatalf("markdown:\n%q\nwant:\n%q", md, want)
	}
}
