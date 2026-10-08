package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	codefly "github.com/codefly-dev/sdk-go"
)

func observabilityEnabled() bool {
	return strings.TrimSpace(workspaceEnv("observability", "OTEL_EXPORTER_OTLP_ENDPOINT")) != ""
}

// workspaceEnv resolves Codefly workspace configuration and secret values
// before falling back to a raw environment variable. Services must use this
// boundary for declared workspace-configuration-dependencies; Codefly keeps
// those values namespaced to prevent collisions between providers.
func workspaceEnv(configuration, key string) string {
	if value, err := codefly.For(codefly.Context()).WorkspaceValue(configuration, key); err == nil && value != "" {
		return value
	}
	return os.Getenv(key)
}

// Only targets whose handlers enforce Work Context scopes may be enabled.
// Empty configuration leaves all federated routes on bearer authentication.
func parseHeadlessModulePrefixes(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	var prefixes []string
	if err := json.Unmarshal([]byte(raw), &prefixes); err != nil || prefixes == nil {
		return nil, fmt.Errorf("WORK_CONTEXT_MODULE_PREFIXES must be a JSON array")
	}
	for _, prefix := range prefixes {
		if !validCatalogIdentity(prefix) || out[prefix] {
			return nil, fmt.Errorf("WORK_CONTEXT_MODULE_PREFIXES requires unique exact module prefixes")
		}
		out[prefix] = true
	}
	return out, nil
}
