.PHONY: build vet test bench fmt check install clean dist dist-npm

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o routre .

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
	install -Dm755 routre $(PREFIX)/bin/routre
	install -Dm644 config.example.json $(PREFIX)/etc/routre/config.json
	install -Dm644 deploy/routre.service $(PREFIX)/lib/systemd/system/routre.service
	install -Dm644 deploy/routre.socket $(PREFIX)/lib/systemd/system/routre.socket
	@echo "installed; enable with:"
	@echo "  systemctl daemon-reload"
	@echo "  systemctl enable --now routre"

# Build the npm distribution (six platform packages + the launcher).
# Requires Go and npm on PATH. Output: npm/dist/*.tgz
dist-npm:
	node npm/build.mjs

# Local mirror of .github/workflows/release.yml: same asset names
# (routre_{GOOS}_{GOARCH}.tar.gz / .zip + checksums.txt) so install.sh
# and `routre update` behave identically against a hand-made release.
dist-release:
	@set -eu; mkdir -p dist-release; rm -f dist-release/routre*
	@build() { \
	  goos=$$1; goarch=$$2; arc=$$3; \
	  CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch go build -trimpath \
	    -ldflags "-s -w -X main.version=$(VERSION)" -o dist-release/routre . ; \
	  if [ "$$goos" = windows ]; then \
	    mv dist-release/routre dist-release/routre.exe; \
	    (cd dist-release && zip -q routre_windows_$${arc}.zip routre.exe); \
	    rm dist-release/routre.exe; \
	  else \
	    tar czf dist-release/routre_$${goos}_$${arc}.tar.gz -C dist-release routre; \
	    rm dist-release/routre; \
	  fi; }; \
	build linux amd64 amd64; build linux arm64 arm64; \
	build darwin amd64 amd64; build darwin arm64 arm64; \
	build windows amd64 amd64; build windows arm64 arm64; \
	cd dist-release && sha256sum routre_*.tar.gz routre_*.zip > checksums.txt
	@echo "release assets in dist-release/"

dist: dist-npm

clean:
	rm -f routre bench-results.txt
	rm -rf npm/dist npm/routre/binary
	rm -rf npm/routre-*/binary
