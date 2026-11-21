package main

import (
	"log/slog"
	"os"

	codefly "github.com/codefly-dev/sdk-go"
)

// workspaceEnv resolves Codefly workspace configuration and secret values
// and falls back to a raw environment variable only when neither is set.
// A failed read leaves the setting unavailable. Services must use this
// boundary for declared workspace-configuration-dependencies; Codefly keeps
// those values namespaced to prevent collisions between providers.
func workspaceEnv(configuration, key string) string {
	value, found, err := codefly.For(codefly.Context()).WorkspaceValueIfSet(configuration, key)
	if err != nil {
		slog.Warn("workspace configuration is unavailable", "configuration", configuration, "key", key)
		return ""
	}
	if found {
		return value
	}
	return os.Getenv(key)
}
