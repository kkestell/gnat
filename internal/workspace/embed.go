package workspace

import "embed"

// binaries holds the linux gnat-container binaries `make linux` builds into bin/, named
// gnat-container-linux-ARCH.
//
//go:embed all:bin
var binaries embed.FS
