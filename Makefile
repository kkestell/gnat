.PHONY: build install ui release test lint clean

# V2 embeds its browser assets directly from internal/v2/daemon. No asset build
# or container helper is needed for a native build or install.
build:
	go build -o gnat ./cmd/gnat

install:
	go install ./cmd/gnat

# Retained v1 dashboard sources; not used by the v2 distribution.
ui:
	cd ui && bun install && bun run build

# Native Apple-silicon distribution. Override VERSION when building a release
# without a tag checkout; the release workflow supplies its exact tag.
VERSION ?= v2-dev
RELEASE := gnat-darwin-arm64

release:
	rm -rf dist/$(RELEASE) dist/$(RELEASE).tar.gz dist/$(RELEASE).tar.gz.sha256
	mkdir -p dist/$(RELEASE)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w -X github.com/kkestell/gnat/internal/v2/entry.Version=$(VERSION)" -o dist/$(RELEASE)/gnat ./cmd/gnat
	tar -C dist -czf dist/$(RELEASE).tar.gz $(RELEASE)
	cd dist && shasum -a 256 $(RELEASE).tar.gz > $(RELEASE).tar.gz.sha256

test:
	go test ./...
	bun test internal/v2/daemon/page.test.js
	cd ui && bun test

lint:
	go vet ./...
	staticcheck ./...
	test -z "$$(gofmt -l .)"
	cd ui && bun run typecheck

clean:
	rm -rf gnat dist
