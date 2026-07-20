package main

import (
	"os"
	"strings"
)

// workspaceEnv resolves Codefly workspace configuration and secret values
// before falling back to a raw environment variable. Services must use this
// boundary for declared workspace-configuration-dependencies; Codefly keeps
// those values namespaced to prevent collisions between providers.
func workspaceEnv(configuration, key string) string {
	key = strings.ToUpper(key)
	exact := strings.ToUpper(configuration)
	normalized := strings.ReplaceAll(exact, "-", "_")
	for _, prefix := range []string{exact, normalized} {
		if value := os.Getenv("CODEFLY__WORKSPACE_CONFIGURATION__" + prefix + "__" + key); value != "" {
			return value
		}
		if value := os.Getenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__" + prefix + "__" + key); value != "" {
			return value
		}
		if exact == normalized {
			break
		}
	}
	return os.Getenv(key)
}
