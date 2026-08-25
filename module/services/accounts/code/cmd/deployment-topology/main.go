package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"accounts/pkg/cataloggen"
)

func main() {
	catalogPath := flag.String("catalog", "", "path to normalized service catalog JSON")
	configPath := flag.String("config", "", "path to deployment topology bindings YAML")
	frontendPluginAllowlistPath := flag.String("frontend-plugin-allowlist", "", "optional path to generated frontend plugin service allowlist JSON")
	applicationConfigPath := flag.String("application-config", "", "optional path to application-owned deployment bindings YAML")
	topologyOutput := flag.String("topology-output", "", "path for normalized deployment topology JSON")
	moduleOutput := flag.String("module-output", "", "path for generated module.codefly.yaml")
	servicesRoot := flag.String("services-root", "", "root containing generated service manifests")
	networkOutput := flag.String("network-output", "", "path for generated Kubernetes NetworkPolicy YAML")
	meshOutput := flag.String("mesh-output", "", "path for generated Istio mesh policy YAML")
	flag.Parse()

	if *catalogPath == "" || *configPath == "" || *topologyOutput == "" || *moduleOutput == "" || *servicesRoot == "" || *networkOutput == "" || *meshOutput == "" {
		_, _ = fmt.Fprintln(os.Stderr, "compile deployment topology: all input and output flags are required")
		os.Exit(2)
	}
	serviceDocument := mustRead(*catalogPath, "service catalog")
	bindingDocument := mustRead(*configPath, "deployment topology bindings")
	var artifacts *cataloggen.DeploymentArtifacts
	var err error
	applicationDocument := readOptional(*applicationConfigPath)
	if *frontendPluginAllowlistPath == "" && len(applicationDocument) == 0 {
		artifacts, err = cataloggen.BuildDeploymentArtifacts(serviceDocument, bindingDocument)
	} else {
		allowlistDocument := []byte(`{"schemaVersion":1,"contractVersion":1,"entries":[]}`)
		if *frontendPluginAllowlistPath != "" {
			allowlistDocument = mustRead(*frontendPluginAllowlistPath, "frontend plugin service allowlist")
		}
		artifacts, err = cataloggen.BuildDeploymentArtifactsWithApplicationBindings(
			serviceDocument,
			bindingDocument,
			allowlistDocument,
			applicationDocument,
		)
	}
	if err != nil {
		fatal("compile deployment topology", err)
	}

	mustWrite(*topologyOutput, artifacts.CatalogJSON)
	mustWrite(*moduleOutput, artifacts.ModuleManifest)
	mustWrite(*networkOutput, artifacts.NetworkPolicy)
	mustWrite(*meshOutput, artifacts.MeshPolicy)
	serviceNames := make([]string, 0, len(artifacts.ServiceManifests))
	for service := range artifacts.ServiceManifests {
		serviceNames = append(serviceNames, service)
	}
	sort.Strings(serviceNames)
	for _, service := range serviceNames {
		mustWrite(filepath.Join(*servicesRoot, service, "service.codefly.yaml"), artifacts.ServiceManifests[service])
	}
}

func readOptional(path string) []byte {
	if path == "" {
		return nil
	}
	document, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		fatal("read optional application deployment bindings", err)
	}
	return document
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
