package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"accounts/pkg/cataloggen"
)

func main() {
	catalogPath := flag.String("catalog", "", "path to normalized service catalog JSON")
	topologyPath := flag.String("topology", "", "path to deployment topology bindings YAML")
	allowlistPath := flag.String("allowlist", "", "path to generated frontend plugin service allowlist JSON")
	outputPath := flag.String("output", "", "path for generated frontend service.codefly.yaml")
	check := flag.Bool("check", false, "verify that the output is current without writing it")
	flag.Parse()

	if *catalogPath == "" || *topologyPath == "" || *allowlistPath == "" || *outputPath == "" {
		_, _ = fmt.Fprintln(os.Stderr, "generate frontend service manifest: all input and output flags are required")
		os.Exit(2)
	}

	artifacts, err := cataloggen.BuildDeploymentArtifactsWithFrontendPluginAllowlist(
		mustRead(*catalogPath, "service catalog"),
		mustRead(*topologyPath, "deployment topology bindings"),
		mustRead(*allowlistPath, "frontend plugin service allowlist"),
	)
	if err != nil {
		fatal("generate frontend service manifest", err)
	}
	document, exists := artifacts.ServiceManifests["frontend"]
	if !exists {
		fatal("generate frontend service manifest", fmt.Errorf("deployment topology has no frontend service"))
	}

	if *check {
		current, err := os.ReadFile(*outputPath)
		if err != nil {
			fatal("read generated frontend service manifest", err)
		}
		if !bytes.Equal(current, document) {
			fatal(
				"check generated frontend service manifest",
				fmt.Errorf("%s is stale; run npm run generate:plugin-codefly-dependencies in services/frontend/code", *outputPath),
			)
		}
		return
	}

	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		fatal("create frontend service manifest directory", err)
	}
	if err := os.WriteFile(*outputPath, document, 0o644); err != nil {
		fatal("write frontend service manifest", err)
	}
}

func mustRead(path, name string) []byte {
	document, err := os.ReadFile(path)
	if err != nil {
		fatal("read "+name, err)
	}
	return document
}

func fatal(operation string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
	os.Exit(1)
}
