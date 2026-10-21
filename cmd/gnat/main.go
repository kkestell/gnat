// Command gnat runs a swarm of coding agents, each in its own Docker container.
package main

import (
	"os"

	"github.com/kkestell/gnat/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
