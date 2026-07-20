package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"accounts/pkg/cataloggen"
)

func main() {
	catalogPath := flag.String("catalog", "", "path to normalized service catalog JSON")
	topologyPath := flag.String("topology", "", "path to normalized deployment topology JSON")
	configPath := flag.String("config", "", "path to frontend plugin bindings YAML")
	codeRoot := flag.String("code-root", "", "path to the frontend code root")
	catalogOutput := flag.String("catalog-output", "", "path for normalized frontend plugin catalog JSON")
	typescriptOutput := flag.String("typescript-output", "", "path for generated frontend plugin TypeScript")
	flag.Parse()

	if *catalogPath == "" || *topologyPath == "" || *configPath == "" || *codeRoot == "" || *catalogOutput == "" || *typescriptOutput == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile frontend plugins: all input and output flags are required")
		os.Exit(2)
	}
	artifacts, err := cataloggen.BuildFrontendPluginArtifacts(
		mustRead(*catalogPath, "service catalog"),
		mustRead(*topologyPath, "deployment topology"),
		mustRead(*configPath, "frontend plugin bindings"),
		*codeRoot,
	)
	if err != nil {
		fatal("compile frontend plugins", err)
	}
	mustWrite(*catalogOutput, artifacts.CatalogJSON)
	mustWrite(*typescriptOutput, artifacts.TypeScript)
}

func mustRead(path, name string) []byte {
	document, err := os.ReadFile(path)
	if err != nil {
		fatal("read "+name, err)
	}
	return document
}

func mustWrite(path string, document []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal("create output directory", err)
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		fatal("write "+path, err)
	}
}

func fatal(operation string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
	os.Exit(1)
}
