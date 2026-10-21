.PHONY: build install test lint

# Build the gnat binary into the checkout.
build:
	go build -o gnat ./cmd/gnat

# Install gnat from this checkout; rerun after a pull to pick up changes.
# Placeholder: the cli work adds the embedded linux binaries for containers.
install:
	go install ./cmd/gnat

test:
	go test ./...

lint:
	go vet ./...
	staticcheck ./...
	test -z "$$(gofmt -l .)"
