package main

import (
	"fmt"
	"os"

	"heckel.io/hostit/appcli"
)

// Set at build time via -ldflags "-X main.version=..."
var (
	version = "dev"
)

func main() {
	if err := appcli.New(version).Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err.Error())
		os.Exit(1)
	}
}
