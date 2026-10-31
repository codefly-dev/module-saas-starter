// Package plugins is accounts' plugin registry.
//
// STOPGAP (codefly-dev/service-go-grpc#223): this file should be the agent's
// plugins/registry_gen.go, rendered from plugins/plugins.yaml. The agent renders
// a registry only alongside its scaffold, and the scaffold needs a single
// AccountsService that this multi-service layout does not have. The agent
// refuses a plugins.yaml it cannot render, so the registry is hand-written
// until #223 lands. Add a plugin by adding it to All.
package plugins

import (
	"accounts/pkg/framework"
	"accounts/pkg/permissionsplugin"
)

// All returns the list of registered plugins.
//
// **Wiring note for operators.** The permissions plugin is
// constructed without signing keys here so this registry has
// no dependency on operator config. main.go (or work.go) is
// responsible for configuring the shared instance via
// Plugin.WithHMACSecret / Plugin.WithEd25519Key before NewServer.
//
// Pattern in main.go:
//
//	permissionsplugin.Default().
//	    WithHMACSecret(loadSpawnSecret()).
//	    WithEd25519Key(loadEd25519PrivateKey())
//	server, _ := adapters.NewServer(config, ...)
func All() []framework.Plugin {
	return []framework.Plugin{
		permissionsplugin.New(),
	}
}
