.PHONY: build install release test lint clean

# Browser assets are embedded directly from internal/daemon.
build:
	go build -o gnat ./cmd/gnat

install:
	go install ./cmd/gnat

# Native Apple-silicon distribution. Override VERSION when building a release
# without a tag checkout; the release workflow supplies its exact tag.
VERSION ?= dev
RELEASE := gnat-darwin-arm64

release:
	rm -rf dist/$(RELEASE) dist/$(RELEASE).tar.gz dist/$(RELEASE).tar.gz.sha256
	mkdir -p dist/$(RELEASE)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w -X github.com/kkestell/gnat/internal/entry.Version=$(VERSION)" -o dist/$(RELEASE)/gnat ./cmd/gnat
	tar -C dist -czf dist/$(RELEASE).tar.gz $(RELEASE)
	cd dist && shasum -a 256 $(RELEASE).tar.gz > $(RELEASE).tar.gz.sha256

test:
	go test ./...
	bun test internal/daemon/page.test.js

lint:
	go vet ./...
	staticcheck ./...
	test -z "$$(gofmt -l .)"

clean:
	rm -rf gnat dist
