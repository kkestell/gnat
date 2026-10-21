.PHONY: build install linux test lint clean

# The linux binaries containers run, embedded in the gnat binary and copied to
# /opt/gnat/gnat by `gnat up`: static, for each architecture Docker may run on. The
# `container` tag leaves out the embed, so they don't contain themselves.
BIN_DIR := internal/workspace/bin
ARCHES := amd64 arm64

linux:
	for arch in $(ARCHES); do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -tags container -trimpath -ldflags="-s -w" \
			-o $(BIN_DIR)/gnat-linux-$$arch ./cmd/gnat || exit 1; \
	done

# Build the gnat binary into the checkout.
build: linux
	go build -o gnat ./cmd/gnat

# Install gnat from this checkout; rerun after a pull to pick up changes.
install: linux
	go install ./cmd/gnat

test:
	go test ./...

lint:
	go vet ./...
	staticcheck ./...
	test -z "$$(gofmt -l .)"

clean:
	rm -f gnat $(BIN_DIR)/gnat-linux-*
