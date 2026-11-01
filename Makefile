.PHONY: build test run lint validate-data

BUILD_DIR=build
export CGO_ENABLED=0

build:
	go build -trimpath -o $(BUILD_DIR)/bluefin-report ./cmd/bluefin-report

# -race needs cgo.
test:
	CGO_ENABLED=1 go test -race ./...

run: build
	./$(BUILD_DIR)/bluefin-report --dry-run

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...

validate-data:
	desktop-file-validate data/io.projectbluefin.Report.desktop
	@if command -v appstreamcli >/dev/null 2>&1; then \
		appstreamcli validate --no-net data/io.projectbluefin.Report.metainfo.xml; \
	else echo "==> skipping metainfo: appstreamcli not installed"; fi
