package main

import (
	"fmt"
	"io"
	"os"

	"github.com/inwake/intraspect/internal/mcpserver"
)

func main() {
	os.Exit(run(mcpserver.ServeStdio, os.Stderr))
}

func run(serve func() error, stderr io.Writer) int {
	if err := serve(); err != nil {
		fmt.Fprintf(stderr, "intraspect: %v\n", err)
		return 1
	}
	return 0
}
