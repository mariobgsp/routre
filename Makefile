.PHONY: build vet test bench fmt check install clean dist dist-npm

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o routre-cli .

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

dist: dist-npm

clean:
	rm -f routre-cli bench-results.txt
	rm -rf npm/dist npm/routre-cli/binary
	rm -rf npm/routre-cli-*/binary
