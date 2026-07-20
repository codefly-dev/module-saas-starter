package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"accounts/pkg/cataloggen"
)

func main() {
	catalogPath := flag.String("catalog", "", "path to the normalized service catalog JSON")
	outputPath := flag.String("output", "", "write generated TypeScript to this path (stdout when empty)")
	flag.Parse()

	if *catalogPath == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile frontend catalog: -catalog is required")
		os.Exit(2)
	}
	catalog, err := os.ReadFile(*catalogPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read service catalog: %v\n", err)
		os.Exit(1)
	}
	document, err := cataloggen.RenderFrontendCatalog(catalog)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "compile frontend catalog: %v\n", err)
		os.Exit(1)
	}
	if *outputPath == "" {
		_, _ = os.Stdout.Write(document)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create frontend catalog directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outputPath, document, 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write frontend catalog: %v\n", err)
		os.Exit(1)
	}
}
