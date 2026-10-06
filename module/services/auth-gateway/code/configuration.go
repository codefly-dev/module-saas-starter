package main

import (
	"os"

	codefly "github.com/codefly-dev/sdk-go"
)

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
