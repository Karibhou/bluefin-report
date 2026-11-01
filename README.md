# Report a problem (bluefin-report)

A GTK4/Libadwaita app that turns a [`systemd-report`](https://www.freedesktop.org/software/systemd/man/latest/systemd-report.html)
snapshot into a reviewed, privacy-scrubbed GitHub issue. You see exactly what
leaves the machine before anything is sent; it is published with your own `gh`
login as an issue in the right Bluefin repository, with the full report attached.

## Requirements

- systemd 261 or newer with `systemd-report` (Dakota ships 262, Utah 261)
- GTK 4 and libadwaita 1 (≥ 1.6) shared libraries from your OS
- [`gh`](https://cli.github.com/) — sign-in happens inside the app

## Install

```bash
brew install --cask projectbluefin/tap/bluefin-report
```

## Usage

```bash
bluefin-report                   # open the app
bluefin-report --dry-run         # full flow, but gh calls are logged, not run
bluefin-report --print-summary   # collect a snapshot and print the overview, no UI
```

Browse All Metrics filters the collected snapshot as you type. Each row has a
Copy button for its complete value, including text shortened on screen. An empty
search result explains how to recover; an empty collection is shown separately.

## Privacy

- Removed before you ever see it: hostname, machine ID, boot ID, machine-info,
  hardware serial numbers, system UUID and asset tags, report-signing keys,
  your username, MAC/IPv4/IPv6 addresses, email addresses, and home paths
  (`/home/<user>` → `~`). Pattern redaction is best-effort; the Review page
  lists every removal with a count.
- Never sent unless you opt in per entry: file contents (`io.systemd.Files.*`)
  and user-session metrics.
- Diagnostics, each off by default and not even collected unless switched on in Compose:
  errors from this boot (`journalctl -b0 -p err`, last 40 lines), crashes in the last
  7 days (`coredumpctl list`), enabled GNOME extensions, installed Flatpak apps. They are
  scrubbed like the report and attached under `extras`; errors and crashes also appear
  collapsed in the issue text.
- Always shown: image ref, tag and manifest digest, CPU model, GPUs (PCI IDs and driver),
  GNOME Shell version.
- Sent byte-for-byte as shown in Review; editing anything withdraws consent.
  An unfinished report can be retried under the same ID only with its unchanged
  reviewed draft (including its report, area, and screenshots). Resume from Drafts
  publishes that unchanged saved version; start a new report to publish edits instead.
  Unreadable or corrupt saved state is not overwritten.
- The full report is attached publicly to the issue as `systemd-report.json`
  (an issue artifact): anyone who can see the issue can read it. Only if GitHub
  refuses artifacts does it fall back to a secret gist linked in an issue comment.
- Screenshots (png, jpg, jpeg, gif; up to 10, 10 MiB each) are re-encoded to strip
  metadata, but their visible content is not scrubbed and is public. They go in one
  issue comment (Confirm: on the one "Same problem here." comment, which also names
  your report, attached to that issue as `systemd-report-<reportID>.json`) and are
  copied into the draft until published.
- Raw snapshots stay in memory. Drafts keep only the reviewed payload under
  `$XDG_STATE_HOME/bluefin-report/drafts/` (mode 0700/0600). Discarding a
  draft never deletes anything already on GitHub.

## Development

```bash
make test           # go test -race ./...
make run            # build and run with --dry-run
make lint           # gofmt + go vet
make validate-data  # desktop-file-validate (+ appstreamcli if present)
make bump           # tag the next release, YY.MM.N (does not push)
```

Releases use calendar versions `YY.MM.N` and are cut deliberately with `make bump`,
not per commit. `bluefin-report --version` prints the running version.

Licensed under Apache-2.0.
