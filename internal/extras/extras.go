// Package extras collects what systemd-report does not: image identity for the Overview,
// and opt-in diagnostic sections. Every probe is unprivileged and tolerant of failure:
// a missing tool or file yields "" rather than an error.
package extras

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Identity is shown in the Overview and the issue summary table.
type Identity struct{ Ref, Tag, Digest, CPU, GPU, GNOME string }

// Root prefixes every file read; tests point it at fixtures.
var Root = "/"

// run is replaced in tests; output is capped at 64 KiB.
var run = func(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	if len(out) > 64<<10 {
		out = out[len(out)-64<<10:] // keep the newest lines
	}
	return strings.TrimSpace(string(out))
}

func read(p string) string {
	b, _ := os.ReadFile(filepath.Join(Root, p))
	return string(b)
}

// Probe collects the identity. Call it off the main thread.
func Probe(ctx context.Context) Identity {
	id := deployID(read("proc/cmdline"))
	return Identity{
		Ref:    run(ctx, "/usr/libexec/ublue-booted-image", "ref"),
		Tag:    run(ctx, "/usr/libexec/ublue-booted-image", "tag"),
		Digest: originDigest(read(filepath.Join("sysroot/state/deploy", id, id+".origin"))),
		CPU:    cpuModel(read("proc/cpuinfo")),
		GPU:    gpus(),
		GNOME:  strings.TrimPrefix(run(ctx, "gnome-shell", "--version"), "GNOME Shell "),
	}
}

var reDigest = regexp.MustCompile(`(?:^|\s)composefs\.digest=(?:[^:\s]*:)?([0-9a-f]+)(?:\s|$)`)

// deployID returns the booted composefs deployment id from /proc/cmdline, or "".
func deployID(cmdline string) string {
	if m := reDigest.FindStringSubmatch(cmdline); m != nil {
		return m[1]
	}
	return ""
}

// originDigest returns [image] manifest_digest from a deployment .origin file.
func originDigest(origin string) string {
	section := ""
	for _, l := range strings.Split(origin, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			section = l
		} else if k, v, ok := strings.Cut(l, "="); ok && section == "[image]" && strings.TrimSpace(k) == "manifest_digest" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func cpuModel(cpuinfo string) string {
	for _, l := range strings.Split(cpuinfo, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var vendors = map[string]string{"1002": "AMD", "10DE": "NVIDIA", "8086": "Intel"}

// gpu formats one DRM card's uevent as "AMD 1002:744C (amdgpu)".
func gpu(uevent string) string {
	var id, drv string
	for _, l := range strings.Split(uevent, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "PCI_ID":
			id = strings.ToUpper(v)
		case "DRIVER":
			drv = v
		}
	}
	if id == "" {
		return ""
	}
	s := id
	if v, ok := vendors[id[:strings.Index(id+":", ":")]]; ok {
		s = v + " " + id
	}
	if drv != "" {
		s += " (" + drv + ")"
	}
	return s
}

var reCard = regexp.MustCompile(`^card[0-9]+$`) // not connectors like card0-DP-1

func gpus() string {
	ents, _ := os.ReadDir(filepath.Join(Root, "sys/class/drm"))
	var out []string
	for _, e := range ents {
		if reCard.MatchString(e.Name()) {
			if g := gpu(read(filepath.Join("sys/class/drm", e.Name(), "device/uevent"))); g != "" {
				out = append(out, g)
			}
		}
	}
	return strings.Join(out, ", ")
}

// Section is one opt-in block. Details marks those also shown, collapsed, in the issue body.
type Section struct {
	Key, Title string
	Details    bool
	cmd        []string
}

var Sections = []Section{
	{"bootErrors", "Errors From This Boot", true, []string{"journalctl", "-b0", "-p", "err", "--no-pager", "-n", "40", "-o", "short", "--no-hostname"}},
	{"crashes", "Crashes in the Last 7 Days", true, []string{"coredumpctl", "list", "--no-pager", "--since", "-7d"}},
	{"gnomeExtensions", "Enabled GNOME Extensions", false, []string{"gnome-extensions", "list", "--enabled"}},
	{"flatpaks", "Installed Flatpak Apps", false, []string{"flatpak", "list", "--app", "--columns=application,version"}},
}

// Collect runs only the sections whose keys are given. Call it off the main thread.
func Collect(ctx context.Context, keys []string) map[string]string {
	out := map[string]string{}
	for _, s := range Sections {
		for _, k := range keys {
			if k == s.Key {
				out[k] = run(ctx, s.cmd[0], s.cmd[1:]...)
			}
		}
	}
	return out
}

// Markdown renders the Details sections present in vals as collapsed blocks.
func Markdown(vals map[string]string) string {
	var b strings.Builder
	for _, s := range Sections {
		v, ok := vals[s.Key]
		if !ok || !s.Details {
			continue
		}
		if v == "" {
			v = "(none)"
		}
		fmt.Fprintf(&b, "\n\n<details><summary>%s</summary>\n\n```\n%s\n```\n\n</details>", s.Title, strings.ReplaceAll(v, "```", "'''"))
	}
	return b.String()
}
