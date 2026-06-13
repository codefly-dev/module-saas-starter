package adapters

import (
	"google.golang.org/grpc"

	"api/pkg/gen"
	"api/pkg/permissionsplugin"
)

// init wires adapters.RegisterExtras as the late-bound
// GRPCRegistrar that permissionsplugin invokes when its
// RegisterGRPC is called by the plugin loop in server_gen.go.
//
// **Why a function-pointer hand-off.** server_gen.go (also in
// adapters) imports api/plugins, plugins imports
// permissionsplugin. If permissionsplugin imported adapters
// directly to call RegisterExtras, we'd close a cycle. Setting
// the function pointer in adapters' init() keeps the
// permissionsplugin → adapters edge OUT of the type-checker
// graph while still giving us a single canonical registration
// path at runtime.
func init() {
	permissionsplugin.GRPCRegistrar = RegisterExtras
}

// =====================================================================
// Manual registration for services NOT yet in grpc_gen.go
// =====================================================================
//
// The auto-generated grpc_gen.go (produced by the codefly agent
// when scaffolding the saas-starter API) registers the original
// set of services. New services added to the proto AFTER that
// scaffold ran (PrincipalService, DelegationService) need
// manual wiring until the codefly agent regenerates grpc_gen.go.
//
// **Why not just regen grpc_gen.go.** We could, but that
// generation flow has its own life-cycle (codefly agent versions,
// tooling); making this file a one-liner glue keeps the proto-
// regen step decoupled from the wire registration.
//
// **What this file does.**
//
//   - Defines PrincipalServer (already lives in principal_rpcs.go;
//     declared via Unsafe* embedding to satisfy the gRPC stub)
//   - Defines DelegationServer (already lives in delegation_rpcs.go)
//   - Provides RegisterExtras to wire them into a *grpc.Server
//
// Caller (main.go, wherever NewGrpServer's result is finalized)
// invokes RegisterExtras AFTER NewGrpServer returns. Idempotent
// is irrelevant: each Register* must be called exactly once per
// gRPC server.

// principalSingleton + delegationSingleton are shared across
// the gRPC and Connect protocol entry points. Two reasons:
//
//   1. DelegationServer holds an in-memory mintCache that links
//      DecideDelegation (which mints a token on approve) and
//      WaitForDelegation (which surfaces the freshly-minted
//      token to streaming clients). Splitting the server across
//      protocols would split the cache — a Decide via gRPC
//      followed by a Wait via Connect would surface an empty
//      token. Sharing one instance keeps the cache visible.
//
//   2. PrincipalServer is stateless today, but a future field
//      (audit emitter, decision cache, etc.) would silently
//      diverge across protocols if instances were per-protocol.
//      Cheap to singleton now, expensive to retrofit later.
//
// Initialised lazily by RegisterExtras / DelegationSingleton().
// Connect adapters reach in via DelegationSingleton() so they
// pick up the same keys the plugin configured.
var (
	principalSingleton  = &PrincipalServer{}
	delegationSingleton = &DelegationServer{}
)

// PrincipalSingleton returns the shared PrincipalServer instance.
func PrincipalSingleton() *PrincipalServer { return principalSingleton }

// DelegationSingleton returns the shared DelegationServer instance.
// Connect handlers in connect_gen.go use this so DecideDelegation
// (called via gRPC) and WaitForDelegation (called via Connect)
// share one mintCache.
func DelegationSingleton() *DelegationServer { return delegationSingleton }

// RegisterExtras attaches the post-scaffold gRPC services to s.
// Call AFTER NewGrpServer returns and BEFORE serving.
//
// signingSecret is the v1-hmac key for DelegationServer's
// approve-mint flow. signingEd25519Key (if non-empty) takes
// precedence and produces v2 tokens. Either / both can be empty
// for non-production environments where DecideDelegation is not
// expected to mint tokens (M7 escalation flow disabled).
//
// Writes the keys onto the singleton so Connect handlers built
// later in NewConnectServer pick up the same configuration
// without having to re-load secrets.
func RegisterExtras(s *grpc.Server, signingSecret []byte, signingEd25519Key []byte) {
	delegationSingleton.SigningSecret = signingSecret
	delegationSingleton.SigningEd25519Key = signingEd25519Key

	gen.RegisterPrincipalServiceServer(s, principalSingleton)
	gen.RegisterDelegationServiceServer(s, delegationSingleton)
}
