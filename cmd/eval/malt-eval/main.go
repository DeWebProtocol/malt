// Package main provides the unified MALT evaluation CLI.
package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/cmd/eval/command"
)

func main() {
	if err := command.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
