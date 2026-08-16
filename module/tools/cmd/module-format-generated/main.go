package main

import (
	"fmt"
	"os"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/generatedgo"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: module-format-generated <root> [<root> ...]")
		os.Exit(2)
	}
	if err := generatedgo.FormatRoots(os.Args[1:]...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
