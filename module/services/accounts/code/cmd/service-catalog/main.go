package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"accounts/pkg/business"
)

func main() {
	output := flag.String("output", "", "write the normalized catalog to this path (stdout when empty)")
	flag.Parse()

	document, err := business.RenderServiceCatalogJSON()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "compile service catalog: %v\n", err)
		os.Exit(1)
	}
	if *output == "" {
		_, _ = os.Stdout.Write(document)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create service catalog directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*output, document, 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write service catalog: %v\n", err)
		os.Exit(1)
	}
}
