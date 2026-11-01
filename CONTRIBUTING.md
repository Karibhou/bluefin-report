# Contributing to Bluefin Report

- Read [AGENTS.md](AGENTS.md) and the [design plan](systemd-report-design-plan.md) first.
- Open an issue before large behavioral changes. Issues and pull requests are driven by Prow;
  see [how issues and PRs work here](https://github.com/projectbluefin/common/blob/main/docs/skills/label-workflow.md).
- Pull request titles and commits follow [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `ci:`, ...). AI-assisted commits carry an `Assisted-by:` trailer.

## Build and test

Needs Go (version in `go.mod`), Make, and a CGO toolchain for the race detector.
GTK 4 and libadwaita are runtime dependencies only.

```sh
make build
make lint test validate-data
./build/bluefin-report --dry-run   # full flow; gh calls are logged, not run
```

Releases: push a `v*` tag; GoReleaser publishes the tarballs and `checksums.txt`,
and the [Homebrew tap](https://github.com/projectbluefin/homebrew-tap) cask picks them up.
