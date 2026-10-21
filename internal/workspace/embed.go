//go:build !container

package workspace

import "embed"

// binaries holds the linux gnat binaries `make install` builds into bin/, named
// gnat-linux-ARCH. The container build (-tags container) embeds nothing, so a binary
// doesn't contain itself.
//
//go:embed all:bin
var binaries embed.FS
