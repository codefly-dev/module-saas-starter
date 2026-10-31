// Package plugins is accounts' plugin registry, and it is hand-written.
//
// Accounts owns its adapters: no proto under proto/ declares a single
// AccountsService for the go-grpc scaffold to adapt, so the agent's Sync keeps
// only code/main.go current and never renders a plugin registry here (the
// agent renders one from plugins/plugins.yaml only alongside its scaffold, and
// refuses a plugins.yaml it cannot render). Add a plugin by adding it to All.
package plugins

import (
	"accounts/pkg/framework"
	"accounts/pkg/permissionsplugin"
)

// All returns the list of registered plugins.
//
// **Wiring note for operators.** The permissions plugin is
// constructed without signing keys here so registry_gen.go has
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
