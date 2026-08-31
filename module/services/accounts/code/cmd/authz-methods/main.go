package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"

	"accounts/pkg/cataloggen"
	catalogv1 "accounts/pkg/gen/saas/catalog/v1"
)

func main() {
	catalogPath := flag.String("catalog", "", "path to normalized service catalog JSON")
	outputPath := flag.String("output", "", "path for normalized authorization catalog JSON")
	goOutputPath := flag.String("go-output", "", "path for auth-gateway generated authorization metadata")
	flag.Parse()

	if *catalogPath == "" || *outputPath == "" || *goOutputPath == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile authorization methods: all input and output flags are required")
		os.Exit(2)
	}
	serviceDocument := mustRead(*catalogPath, "service catalog")
	authz, err := cataloggen.BuildAuthorizationCatalog(serviceDocument)
	if err != nil {
		fatal("compile authorization methods", err)
	}
	authzDocument, err := cataloggen.RenderAuthorizationCatalogJSON(authz)
	if err != nil {
		fatal("render authorization catalog", err)
	}
	serviceCatalog := &catalogv1.ServiceCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(serviceDocument, serviceCatalog); err != nil {
		fatal("decode service catalog for runtime metadata", err)
	}
	goDocument, err := cataloggen.RenderAuthGatewayAuthorizationMetadata(authz, serviceCatalog)
	if err != nil {
		fatal("render auth-gateway authorization metadata", err)
	}
	mustWrite(*outputPath, authzDocument)
	mustWrite(*goOutputPath, goDocument)
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
