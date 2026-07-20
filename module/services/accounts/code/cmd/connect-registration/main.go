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
	configPath := flag.String("config", "", "path to the Connect implementation bindings YAML")
	outputPath := flag.String("output", "", "write generated Connect Go to this path (stdout when empty)")
	grpcOutputPath := flag.String("grpc-output", "", "write generated raw-gRPC Go to this path (disabled when empty)")
	flag.Parse()

	if *catalogPath == "" || *configPath == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile Connect registration: -catalog and -config are required")
		os.Exit(2)
	}
	catalog, err := os.ReadFile(*catalogPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read service catalog: %v\n", err)
		os.Exit(1)
	}
	config, err := os.ReadFile(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read Connect bindings: %v\n", err)
		os.Exit(1)
	}
	document, err := cataloggen.RenderConnectRegistration(catalog, config)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "compile Connect registration: %v\n", err)
		os.Exit(1)
	}
	grpcDocument, err := cataloggen.RenderGRPCRegistration(catalog, config)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "compile gRPC registration: %v\n", err)
		os.Exit(1)
	}
	if *outputPath == "" {
		_, _ = os.Stdout.Write(document)
	} else {
		writeDocument(*outputPath, document, "Connect")
	}
	if *grpcOutputPath != "" {
		writeDocument(*grpcOutputPath, grpcDocument, "gRPC")
	}
}

func writeDocument(path string, document []byte, label string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create %s registration directory: %v\n", label, err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write %s registration: %v\n", label, err)
		os.Exit(1)
	}
}
