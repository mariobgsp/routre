.PHONY: build vet test bench fmt check install clean dist dist-npm

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o routre-cli .

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

bench:
	$(GO) run . bench -config config.example.json -target 90

fmt:
	gofmt -l -w .

check: vet test bench

install: build
	install -Dm755 routre-cli $(PREFIX)/bin/routre-cli
	install -Dm644 config.example.json $(PREFIX)/etc/routre-cli/config.json
	install -Dm644 deploy/routre-cli.service $(PREFIX)/lib/systemd/system/routre-cli.service
	install -Dm644 deploy/routre-cli.socket $(PREFIX)/lib/systemd/system/routre-cli.socket
	@echo "installed; enable with:"
	@echo "  systemctl daemon-reload"
	@echo "  systemctl enable --now routre-cli"

# Build the npm distribution (six platform packages + the launcher).
# Requires Go and npm on PATH. Output: npm/dist/*.tgz
dist-npm:
	node npm/build.mjs

# Local mirror of .github/workflows/release.yml: same asset names
# (routre-cli_{GOOS}_{GOARCH}.tar.gz / .zip + checksums.txt) so install.sh
# and `routre-cli update` behave identically against a hand-made release.
dist-release:
	@set -eu; mkdir -p dist-release; rm -f dist-release/routre-cli*
	@build() { \
	  goos=$$1; goarch=$$2; arc=$$3; \
	  CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath \
	    -ldflags "-s -w -X main.version=$(VERSION)" -o dist-release/routre-cli . ; \
	  if [ "$$goos" = windows ]; then \
	    mv dist-release/routre-cli dist-release/routre-cli.exe; \
	    (cd dist-release && zip -q routre-cli_windows_$${arc}.zip routre-cli.exe); \
	    rm dist-release/routre-cli.exe; \
	  else \
	    tar czf dist-release/routre-cli_$${goos}_$${arc}.tar.gz -C dist-release routre-cli; \
	    rm dist-release/routre-cli; \
	  fi; }; \
	build linux amd64 amd64; build linux arm64 arm64; \
	build darwin amd64 amd64; build darwin arm64 arm64; \
	build windows amd64 amd64; build windows arm64 arm64; \
	cd dist-release && sha256sum routre-cli_*.tar.gz routre-cli_*.zip > checksums.txt
	@echo "release assets in dist-release/"

dist: dist-npm

clean:
	rm -f routre-cli bench-results.txt
	rm -rf npm/dist npm/routre-cli/binary
	rm -rf npm/routre-cli-*/binary
