.PHONY: build install install-profiles linux ui release test lint clean

# The linux binaries containers run, embedded in the gnat binary and copied to
# /opt/gnat/gnat-container by `gnat up`: static, for each architecture Docker may run on.
BIN_DIR := internal/workspace/bin
ARCHES := amd64 arm64

linux:
	for arch in $(ARCHES); do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags="-s -w" \
			-o $(BIN_DIR)/gnat-container-linux-$$arch ./cmd/gnat-container || exit 1; \
	done

# Build the web UI (ui/) into internal/web/dist, which the gnat-container binary embeds. The
# build is checked in, so rerun this and commit the result after changing ui/.
ui:
	cd ui && bun install && bun run build

# Build the gnat binary into the checkout.
build: linux
	go build -o gnat ./cmd/gnat

# Install gnat from this checkout; rerun after a pull to pick up changes.
install: linux install-profiles
	go install ./cmd/gnat

# Seed the config directory without replacing profiles the user has customized.
install-profiles:
	@profile_dir="$${XDG_CONFIG_HOME:-$$HOME/.config}/gnat/profiles"; \
		for kind in agents swarms; do \
			mkdir -p "$$profile_dir/$$kind" || exit 1; \
			for profile in profiles/$$kind/*.yaml; do \
				target="$$profile_dir/$$kind/$${profile##*/}"; \
				if [ ! -e "$$target" ] && [ ! -L "$$target" ]; then \
					cp "$$profile" "$$target" || exit 1; \
				fi; \
			done; \
		done

# Build the macOS arm64 release archive, the gnat binary and the bundled profiles, and its
# checksum into dist/. The release workflow publishes them for each v* tag.
RELEASE := gnat-darwin-arm64

release: linux
	rm -rf dist/$(RELEASE) dist/$(RELEASE).tar.gz dist/$(RELEASE).tar.gz.sha256
	mkdir -p dist/$(RELEASE)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/$(RELEASE)/gnat ./cmd/gnat
	cp -R profiles dist/$(RELEASE)/profiles
	tar -C dist -czf dist/$(RELEASE).tar.gz $(RELEASE)
	cd dist && shasum -a 256 $(RELEASE).tar.gz > $(RELEASE).tar.gz.sha256

test:
	go test ./...
	cd ui && bun test

lint:
	go vet ./...
	staticcheck ./...
	test -z "$$(gofmt -l .)"
	cd ui && bun run typecheck

clean:
	rm -rf gnat dist $(BIN_DIR)/gnat-container-linux-*
