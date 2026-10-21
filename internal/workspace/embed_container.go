//go:build container

package workspace

import "embed"

// binaries is empty in the binary that runs in containers.
var binaries embed.FS
