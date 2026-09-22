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
	configPath := flag.String("config", "", "path to gateway bindings YAML")
	moduleDir := flag.String("module-dir", "", "module root holding module.codefly.yaml and services/*/service.codefly.yaml")
	routeOutput := flag.String("route-output", "", "path for normalized gateway route JSON")
	goOutput := flag.String("go-output", "", "path for auth-gateway generated Go routes")
	flag.Parse()

	if *catalogPath == "" || *configPath == "" || *moduleDir == "" || *routeOutput == "" || *goOutput == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile gateway routes: all input and output flags are required")
		os.Exit(2)
	}
	serviceDocument := mustRead(*catalogPath, "service catalog")
	bindingDocument := mustRead(*configPath, "gateway bindings")
	documents, err := cataloggen.LoadDeploymentDocuments(*moduleDir)
	if err != nil {
		fatal("load module manifests", err)
	}
	routes, err := cataloggen.BuildGatewayRouteCatalog(serviceDocument, bindingDocument, documents)
	if err != nil {
		fatal("compile gateway routes", err)
	}
	routeDocument, err := cataloggen.RenderGatewayRouteCatalogJSON(routes)
	if err != nil {
		fatal("render gateway route catalog", err)
	}
	goDocument, err := cataloggen.RenderAuthGatewayConnectRoutes(routes)
	if err != nil {
		fatal("render auth-gateway routes", err)
	}
	mustWrite(*routeOutput, routeDocument)
	mustWrite(*goOutput, goDocument)
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
