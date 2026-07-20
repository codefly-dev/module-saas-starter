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
	servicePath := flag.String("service-catalog", "", "path to normalized service catalog JSON")
	gatewayPath := flag.String("gateway-catalog", "", "path to normalized gateway route JSON")
	bindingsPath := flag.String("bindings", "", "path to strict REST implementation bindings YAML")
	rawOpenAPIPath := flag.String("raw-openapi", "", "path to protoc-gen-openapiv2 output")
	surfaceOutput := flag.String("surface-output", "", "path for normalized REST surface JSON")
	accountsOutput := flag.String("accounts-go-output", "", "path for accounts generated REST runtime")
	sidecarOutput := flag.String("sidecar-go-output", "", "path for auth-sidecar generated REST routes")
	openAPIOutput := flag.String("openapi-output", "", "path for filtered public OpenAPI")
	flag.Parse()

	if *servicePath == "" || *gatewayPath == "" || *bindingsPath == "" || *rawOpenAPIPath == "" ||
		*surfaceOutput == "" || *accountsOutput == "" || *sidecarOutput == "" || *openAPIOutput == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile REST surface: all input and output flags are required")
		os.Exit(2)
	}

	serviceDocument := mustRead(*servicePath, "service catalog")
	gatewayDocument := mustRead(*gatewayPath, "gateway catalog")
	bindingDocument := mustRead(*bindingsPath, "REST bindings")
	rawOpenAPI := mustRead(*rawOpenAPIPath, "raw OpenAPI")

	service := &catalogv1.ServiceCatalog{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(serviceDocument, service); err != nil {
		fatal("decode service catalog", err)
	}
	surface, err := cataloggen.BuildRESTSurfaceCatalog(gatewayDocument)
	if err != nil {
		fatal("compile REST surface", err)
	}
	surfaceDocument, err := cataloggen.RenderRESTSurfaceCatalogJSON(surface)
	if err != nil {
		fatal("render REST surface", err)
	}
	accountsRuntime, err := cataloggen.RenderAccountsRESTRuntime(surface, service, bindingDocument)
	if err != nil {
		fatal("render accounts REST runtime", err)
	}
	sidecarRuntime, err := cataloggen.RenderAuthSidecarRESTRoutes(surface)
	if err != nil {
		fatal("render auth-sidecar REST routes", err)
	}
	publicOpenAPI, err := cataloggen.RenderPublicOpenAPI(rawOpenAPI, surface, service)
	if err != nil {
		fatal("render public OpenAPI", err)
	}

	mustWrite(*surfaceOutput, surfaceDocument)
	mustWrite(*accountsOutput, accountsRuntime)
	mustWrite(*sidecarOutput, sidecarRuntime)
	mustWrite(*openAPIOutput, publicOpenAPI)
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
