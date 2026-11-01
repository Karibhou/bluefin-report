# AGENTS.md

Bluefin Report: Go + GTK4/Libadwaita via [puregotk](https://github.com/jwijenbergh/puregotk) (no CGO for builds).
Design and status: [systemd-report-design-plan.md](systemd-report-design-plan.md).

## Layout

- `cmd/bluefin-report`: entry point and flags (`--dry-run`, `--print-summary`)
- `internal/snapshot`: runs `systemd-report` and parses it
- `internal/privacy`: scrubs the payload before anything leaves the machine
- `internal/publish`: gist + issue through `gh`, resumable by report ID
- `internal/ui`: the GTK app
- `data/`: desktop file, AppStream metainfo, icon

## Rules

- puregotk caches callbacks by function-pointer address in a fixed, never-freed table.
  Connect signal handlers once per widget; never create a fresh callback per event.
  Work from goroutines reaches the main thread only through `post()` in `internal/ui/ui.go`,
  which reuses one package-level callback.
- Tests never call the real `gh` for writes. Use `--dry-run` to drive the full flow by hand;
  it logs `gh` calls instead of running them.
- Never weaken the privacy scrubber to make a test pass; the attachment must not contain hostname or username.

## Checks

```sh
make lint test validate-data
```
