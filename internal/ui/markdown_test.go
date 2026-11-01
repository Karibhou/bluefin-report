package ui

import (
	"testing"

	"github.com/projectbluefin/bluefin-report/internal/extras"
	"github.com/projectbluefin/bluefin-report/internal/publish"
	"github.com/projectbluefin/bluefin-report/internal/snapshot"
)

func TestIssueMarkdown(t *testing.T) {
	sum := snapshot.Summary{
		Name: "Bluefin", ImageVersion: "44.20261101", ImageID: "dakota",
		Kernel: "6.17.1", Arch: "x86-64", SystemState: "degraded",
		FailedUnits: []string{"system: a.service", "user: b.service"},
	}
	ident := extras.Identity{Ref: "ghcr.io/projectbluefin/dakota", Tag: "stable", Digest: "abc", CPU: "Ryzen", GPU: "AMD", GNOME: "49"}
	table := "| | |\n|---|---|\n" +
		"| System | Bluefin |\n| Version | 44.20261101 |\n| Image | dakota |\n" +
		"| Kernel | 6.17.1 |\n| Arch | x86-64 |\n| State | degraded |\n" +
		"| Image ref | ghcr.io/projectbluefin/dakota |\n| Tag | stable |\n| Digest | abc |\n" +
		"| CPU | Ryzen |\n| GPU | AMD |\n| GNOME | 49 |\n" +
		"| Failed units | 2 |\n" +
		"\n- `system: a.service`\n- `user: b.service`"
	details := map[string]string{"bootErrors": "oops", "flatpaks": "org.example.App"}
	boot := "\n\n<details><summary>Errors From This Boot</summary>\n\n```\noops\n```\n\n</details>"

	for _, c := range []struct {
		name        string
		mode        publish.Mode
		desc, steps string
		details     map[string]string
		want        string
	}{
		{"new issue", publish.NewIssue, " It broke. \n", " 1. Boot \n", details,
			"It broke.\n\n### Steps to reproduce\n\n1. Boot\n\n" + table + boot},
		{"new issue without text", publish.NewIssue, "  ", "", nil, table},
		{"confirm drops steps", publish.Confirm, "Me too.", "1. Boot", nil,
			"Same problem here.\n\nMe too.\n\n" + table},
		{"update", publish.Update, "", "1. Boot", details,
			"Updated report from the original reporter.\n\n" + table + boot},
	} {
		if got := issueMarkdown(c.mode, c.desc, c.steps, sum, ident, c.details); got != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.name, got, c.want)
		}
	}
}
