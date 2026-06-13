// Package permissionsplugin provides a framework.Plugin
// implementation that registers PrincipalService and
// DelegationService — the post-scaffold gRPC services we added
// for the agent permission system.
//
// **Why a plugin and not direct registration.** server_gen.go is
// auto-generated and iterates plugins.All() for gRPC service
// registration. Adding our services as a plugin keeps the
// integration to ONE line in plugins.yaml + registry_gen.go,
// without touching either generated file's loop.
//
// **What this plugin does:**
//
//   - RegisterGRPC: wires PrincipalServer + DelegationServer
//     onto the gRPC server.
//   - RegisterREST: registers the corresponding grpc-gateway
//     handlers on the REST mux (auto-derives from proto annotations).
//   - Migrations: returns the migration filenames so the codefly
//     migration runner picks them up.
//
// **Operator integration steps** (one-time, post this PR):
//
//   1. Edit `plugins/plugins.yaml`:
//        plugins:
//          - name: permissions
//            import: "api/pkg/permissionsplugin"
//
//   2. Trigger codefly Sync to regenerate registry_gen.go (or
//      update by hand to match the regen output — see
//      plugins/registry.go in this PR).
//
//   3. Configure the signing secret via the Plugin's With*
//      helpers in main.go.
package permissionsplugin

import (
	"context"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"

	"api/pkg/framework"
	"api/pkg/gen"
)

// GRPCRegistrar is the late-bound entry point that wires
// PrincipalServer + DelegationServer onto a gRPC server.
//
// **Why a package-level var.** The handler implementations live
// in package adapters (they share the package-level service
// global with PermServer / UserServer). If permissionsplugin
// imported adapters directly, we'd create a cycle:
// adapters → plugins → permissionsplugin → adapters. Instead,
// adapters writes to GRPCRegistrar in its own init() (no inbound
// edge to permissionsplugin from anywhere else), which inverts
// the edge and breaks the cycle.
//
// Set by adapters/grpc_register_extras.go init(). Nil only in
// test builds that exclude package adapters — RegisterGRPC is a
// no-op in that case (the caller must register handlers
// directly).
var GRPCRegistrar func(server *grpc.Server, signingSecret, signingEd25519Key []byte)

// Plugin is the framework.Plugin implementation for the agent
// permission system. Use New / NewWithKeys to construct.
type Plugin struct {
	signingSecret     []byte
	signingEd25519Key []byte
}

// defaultPlugin is the singleton instance returned by New.
//
// **Why a singleton.** registry_gen.go calls New() to populate
// plugins.All(), but main.go also needs a handle to the plugin
// to wire signing keys via WithHMACSecret / WithEd25519Key
// before NewServer fires the plugin loop. Returning a fresh
// instance from each New() call would mean key configuration
// applied to one copy doesn't reach the registered copy.
//
// All callers operate on the same *Plugin pointer; key writes
// from main.go are visible to RegisterGRPC inside server_gen.go
// because both go through this package-level handle.
var defaultPlugin = &Plugin{}

// New returns the package-level Plugin singleton. Calls before
// keys are configured leave keys empty — DecideDelegation's
// approve path will fail with "no signing key configured" in
// that case. Production wiring lives in main.go (work.go) via
// Default().WithHMACSecret(...).WithEd25519Key(...) prior to
// NewServer.
func New() *Plugin {
	return defaultPlugin
}

// Default exposes the singleton directly. main.go uses this
// to configure signing keys in a typed, panic-free path
// (avoiding the registry-loop type assertion shown in
// registry_gen.go's docs).
func Default() *Plugin {
	return defaultPlugin
}

// NewWithKeys constructs a Plugin with both v1 (HMAC) and v2
// (ed25519) signing keys configured. v2 takes precedence when
// both are set; passing one or the other selects the format
// minted on approve.
//
// signingSecret should be the SAME bytes the codefly host
// distributes to plugins via manager.WithScopedAuthSecret.
// signingEd25519Key is the 64-byte ed25519 private key paired
// with the public key plugins hold via TokenVerifier.
func NewWithKeys(signingSecret []byte, signingEd25519Key []byte) *Plugin {
	return &Plugin{
		signingSecret:     signingSecret,
		signingEd25519Key: signingEd25519Key,
	}
}

// WithHMACSecret returns a copy of p with the HMAC secret set.
// Chainable for fluent configuration.
func (p *Plugin) WithHMACSecret(secret []byte) *Plugin {
	p.signingSecret = secret
	return p
}

// WithEd25519Key returns a copy of p with the ed25519 private
// key set. Chainable.
func (p *Plugin) WithEd25519Key(key []byte) *Plugin {
	p.signingEd25519Key = key
	return p
}

// HMACSecret returns the configured HMAC signing secret. Used by
// the Connect adapter in connect_gen.go to construct a fresh
// DelegationServer with the same keys the gRPC plugin holds —
// avoids a getter-or-setter dance through adapters.
func (p *Plugin) HMACSecret() []byte { return p.signingSecret }

// Ed25519Key returns the configured ed25519 signing key. See
// HMACSecret for the rationale.
func (p *Plugin) Ed25519Key() []byte { return p.signingEd25519Key }

// Name implements framework.Plugin.
func (p *Plugin) Name() string { return "permissions" }

// RegisterGRPC implements framework.Plugin. Wires the two
// post-scaffold gRPC services on the supplied server via the
// late-bound GRPCRegistrar (set by package adapters init).
//
// If GRPCRegistrar is nil (e.g. tests that import this package
// without adapters), RegisterGRPC is a silent no-op — the test
// is responsible for stubbing the handlers it needs.
func (p *Plugin) RegisterGRPC(server *grpc.Server) {
	if GRPCRegistrar == nil {
		return
	}
	GRPCRegistrar(server, p.signingSecret, p.signingEd25519Key)
}

// RegisterREST implements framework.Plugin. Wires the
// grpc-gateway REST handlers — auto-derived from the proto
// HTTP annotations on PrincipalService / DelegationService.
//
// REST endpoints exposed:
//
//   GET    /v1/principals/{id}
//   GET    /v1/principals:byAgent
//   POST   /v1/principals:agent
//   POST   /v1/principals/{id}:revoke
//   GET    /v1/principals
//
//   POST   /v1/delegations
//   GET    /v1/delegations/{id}:wait        ← server-streaming
//   POST   /v1/delegations/{id}:decide
//   GET    /v1/delegations:pending
func (p *Plugin) RegisterREST(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error {
	if err := gen.RegisterPrincipalServiceHandlerFromEndpoint(ctx, mux, endpoint, opts); err != nil {
		return err
	}
	if err := gen.RegisterDelegationServiceHandlerFromEndpoint(ctx, mux, endpoint, opts); err != nil {
		return err
	}
	return nil
}

// Migrations implements framework.Plugin. Returns the migration
// filenames in dependency order. The codefly migration runner
// resolves these via the standard migrations directory.
//
// Migrations 36-39 add the permissions schema:
//   - 36_create_principals: unified identity table
//   - 37_backfill_principals: hydrate from users + api_keys
//   - 38_create_delegation_grants: M7 + M8 grants table
//   - 39_delegation_audit_views: M9 read-only audit views
//
// They're already in the standard migrations directory —
// returning them here is documentation more than orchestration
// (the runner sees them via filesystem scan). Listed in case
// the migration runner is plugin-driven in a future codefly
// version.
func (p *Plugin) Migrations() []string {
	return []string{
		"36_create_principals.up.sql",
		"37_backfill_principals.up.sql",
		"38_create_delegation_grants.up.sql",
		"39_delegation_audit_views.up.sql",
	}
}

// --- Compile-time assertion ---------------------------------------

var _ framework.Plugin = (*Plugin)(nil)
