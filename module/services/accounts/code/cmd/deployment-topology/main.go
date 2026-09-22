package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"accounts/pkg/cataloggen"
)

// The manifests are the model: module.codefly.yaml and every
// services/<name>/service.codefly.yaml are authored and read here; the outputs
// are the projections a cluster needs from them.
func main() {
	catalogPath := flag.String("catalog", "", "path to normalized service catalog JSON")
	moduleDir := flag.String("module-dir", "", "module root holding module.codefly.yaml and services/*/service.codefly.yaml")
	topologyOutput := flag.String("topology-output", "", "path for normalized deployment topology JSON")
	networkOutput := flag.String("network-output", "", "path for generated Kubernetes NetworkPolicy YAML")
	meshOutput := flag.String("mesh-output", "", "path for generated Istio mesh policy YAML")
	flag.Parse()

	if *catalogPath == "" || *moduleDir == "" || *topologyOutput == "" || *networkOutput == "" || *meshOutput == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile deployment topology: all input and output flags are required")
		os.Exit(2)
	}
	serviceDocument := mustRead(*catalogPath, "service catalog")
	documents, err := cataloggen.LoadDeploymentDocuments(*moduleDir)
	if err != nil {
		fatal("load module manifests", err)
	}
	artifacts, err := cataloggen.BuildDeploymentArtifacts(serviceDocument, documents)
	if err != nil {
		fatal("compile deployment topology", err)
	}

	mustWrite(*topologyOutput, artifacts.CatalogJSON)
	mustWrite(*networkOutput, artifacts.NetworkPolicy)
	mustWrite(*meshOutput, artifacts.MeshPolicy)
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
