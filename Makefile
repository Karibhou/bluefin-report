.PHONY: build test run lint validate-data bump screenshots

BUILD_DIR=build
SCREENSHOT_DIR ?= $(BUILD_DIR)/screenshots
PYTHON ?= python3
export CGO_ENABLED=0

build:
	go build -trimpath -o $(BUILD_DIR)/bluefin-report ./cmd/bluefin-report

# -race needs cgo.
test:
	CGO_ENABLED=1 go test -race ./...

run: build
	./$(BUILD_DIR)/bluefin-report --dry-run

# Requires a supported GTK/Libadwaita runtime, AT-SPI, Xvfb, xdotool and ImageMagick.
screenshots: build
	xvfb-run -a -s "-screen 0 1280x1024x24" dbus-run-session -- \
		$(PYTHON) scripts/capture-screenshots.py --binary $(BUILD_DIR)/bluefin-report --output $(SCREENSHOT_DIR)

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...

validate-data:
	desktop-file-validate data/io.projectbluefin.Report.desktop
	@if command -v appstreamcli >/dev/null 2>&1; then \
		appstreamcli validate --no-net data/io.projectbluefin.Report.metainfo.xml; \
	else echo "==> skipping metainfo: appstreamcli not installed"; fi

# Releases are cut deliberately, not per commit: YY.MM.N (scripts/next-version.sh).
# Tag only what main ships, so release notes compare against the previous tag.
bump:
	@git fetch --quiet origin main && \
	if [ "$$(git rev-parse HEAD)" != "$$(git rev-parse origin/main)" ]; then \
		echo "HEAD is not origin/main. Check out and pull main before bumping the version."; \
		exit 1; \
	fi
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "Working directory is not clean. Commit or stash changes before bumping the version."; \
		exit 1; \
	fi
	$(MAKE) lint test
	@version=$$(./scripts/next-version.sh) && \
		git tag -a $$version -m "Version $$version" && \
		echo "Tagged $$version. Publish it with: git push origin $$version"
