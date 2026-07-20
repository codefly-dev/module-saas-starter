package main

import (
	"flag"
	"fmt"
	"os"

	"accounts/pkg/business"
)

func main() {
	output := flag.String("output", "", "write the matrix to this path (stdout when empty)")
	flag.Parse()

	document := business.RenderRPCPolicyMatrix()
	if *output == "" {
		_, _ = os.Stdout.Write(document)
		return
	}
	if err := os.WriteFile(*output, document, 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write authorization matrix: %v\n", err)
		os.Exit(1)
	}
}
