package business

import (
	"fmt"
	"sort"
	"strings"
)

// ModuleAuthorityEndpoint is the name of the accounts endpoint a composed
// module declares a dependency on to reach the internal tier. It is a named
// gRPC endpoint of its own, exported at module visibility in the module
// interface, bound on its own listener, and serving only
// ModuleAuthorityProcedures.
//
// It exists so a module never depends on the private `rest` listener, which
// multiplexes the whole internal tier with the REST surface and is not part of
// this module's interface. See INTERNAL_TRANSPORT.md.
const ModuleAuthorityEndpoint = "authority"

// moduleAuthorityCapabilityService is the one accounts service whose every
// admissible method authenticates the CALLING MODULE itself before it decides
// anything: from a signed capability — its module Work Context, or a viewer
// Work Context whose audience names it — or, for the one workload mint named in
// moduleAuthorityWorkloadMints, from the module pod's projected ServiceAccount
// token, reviewed and bound to its running build. Because the caller is proven
// by the call itself, no perimeter credential is asked of these methods on the
// endpoint: a shared static token would add nothing a forged caller lacks and
// would name no module. Its other Mint* methods are where a module obtains its
// own capability, which the authority interface's Issue serves;
// ValidateModuleAuthorityProcedures refuses them here.
const moduleAuthorityCapabilityService = "/saas.accounts.v1.ModuleCapabilitiesService/"

// moduleAuthorityWorkloadMints are the Mint* methods served on the endpoint.
// Each authenticates the calling module from its workload credential alone —
// the projected ServiceAccount token accounts reviews, the pod it names and the
// build that pod runs — and derives the principal from it, so the request
// cannot name a module it is not.
//
//   - MintSourceOperationContext — the Work Context one connected source's sync
//     runs under, from the delegation the person who connected it gave the
//     module. Datasources are this host's, never the authority interface's, so
//     the module tier is the only place a module can ask for it.
var moduleAuthorityWorkloadMints = map[string]struct{}{
	"/saas.accounts.v1.ModuleCapabilitiesService/MintSourceOperationContext": {},
}

// moduleAuthorityReadOracles are the only procedures admitted on the endpoint
// that do NOT authenticate the caller: they authorize on the perimeter
// credential alone and answer about the principals and tenant the request
// names. Every entry must therefore be READ-ONLY, because the perimeter
// credential is shared by every composed module, so a write here is a write any
// module may make for any tenant.
//
// Each entry, and why it is admissible in spite of that:
//
//   - CheckAuthorizationRevision — answers one bit about whether a capability
//     the consumer already holds is still current. It resolves nothing the
//     caller did not present and mutates nothing.
//   - AuthorizeEvidenceRead — answers one bit about a (caller, owner, task,
//     session) tuple the consumer already holds, and mutates nothing.
//
// Both remain authority oracles over request-named principals, which is a real
// residual property and the reason the set is this small and explicit rather
// than "the WorkContextService consumer seams". Binding them to the calling
// module's own Work Context would remove it; until then nothing may be added
// here that writes.
var moduleAuthorityReadOracles = map[string]struct{}{
	"/saas.accounts.v1.WorkContextService/AuthorizeEvidenceRead":      {},
	"/saas.accounts.v1.WorkContextService/CheckAuthorizationRevision": {},
}

// moduleAuthorityProcedures is the least-privilege subset of the internal tier
// a composed module may call on ModuleAuthorityEndpoint. Every entry is an
// EXPOSURE_INTERNAL method, and every entry either
//
//   - authenticates the calling module itself — from a signed Work Context, or
//     for a workload mint from its workload credential — and authorizes it
//     against the grant its composition declared (the module principal
//     registry). The whole module capability surface does this, and the
//     endpoint asks no perimeter credential of it
//     (ModuleAuthorityAuthenticatesCaller); or
//   - is one of the read-only oracles named in moduleAuthorityReadOracles, on
//     the perimeter credential until each is bound to the calling module.
//
// Deliberately absent, and reachable only on the private listener:
//
//   - the exchanges the auth-gateway brokers for registration (module and
//     solution registration), and the module Work Context and operation-context
//     mints, which the authority interface's Issue serves;
//   - the registries and credential validators only the gateway reads
//     (solution and client registries, API-key validation, identity
//     resolution);
//   - the generic permission oracles and principal administration, which
//     would give a module an authority question about arbitrary principals;
//   - every method that authorizes on the perimeter credential alone AND
//     mutates state, because that credential is shared by every composed
//     module and so names no tenant. ConsumeUsage and StartInstallationTask
//     are the obvious ones. ConsumeSingleUse belongs to the same class and is
//     held off for the same reason: it writes a replay claim keyed on the
//     org_id and context_id the REQUEST carries, with no caller binding, so
//     serving it here would let any module burn any tenant's single-use
//     capability (the legitimate consumer then reads its own valid capability
//     as AlreadyExists) and fill the replay table for tenants it has no
//     relationship with. It joins the endpoint once it decides on a credential
//     bound to the consuming module, exactly as ConsumeUsage does.
//
// Adding a procedure here widens what every composed module can reach;
// ValidateModuleAuthorityProcedures and the test beside this file hold each
// entry to the rules above.
var moduleAuthorityProcedures = []string{
	"/saas.accounts.v1.ModuleCapabilitiesService/ApproveExecutableArtifact",
	"/saas.accounts.v1.ModuleCapabilitiesService/GetCurrentInstallation",
	"/saas.accounts.v1.ModuleCapabilitiesService/AuthorizeExecutableArtifact",
	"/saas.accounts.v1.ModuleCapabilitiesService/RevokeExecutableArtifact",
	"/saas.accounts.v1.ModuleCapabilitiesService/AckJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/CancelApproval",
	"/saas.accounts.v1.ModuleCapabilitiesService/CheckWorkContextRecordAccess",
	"/saas.accounts.v1.ModuleCapabilitiesService/ClaimJobs",
	"/saas.accounts.v1.ModuleCapabilitiesService/DeclareAuditEventTypes",
	"/saas.accounts.v1.ModuleCapabilitiesService/EmitAuditEvent",
	"/saas.accounts.v1.ModuleCapabilitiesService/EnqueueJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/ExchangeDelegatedOperationAudience",
	"/saas.accounts.v1.ModuleCapabilitiesService/ExchangeDelegatedReadAudience",
	"/saas.accounts.v1.ModuleCapabilitiesService/FetchDatasourceBlob",
	"/saas.accounts.v1.ModuleCapabilitiesService/FetchDatasourceFiles",
	"/saas.accounts.v1.ModuleCapabilitiesService/GetApproval",
	"/saas.accounts.v1.ModuleCapabilitiesService/HeartbeatJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/ListReadableSourceCollections",
	"/saas.accounts.v1.ModuleCapabilitiesService/ListSubjectVisibility",
	"/saas.accounts.v1.ModuleCapabilitiesService/ListSubscriptions",
	"/saas.accounts.v1.ModuleCapabilitiesService/MintSourceOperationContext",
	"/saas.accounts.v1.ModuleCapabilitiesService/NackJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/NotifyOrgAdmins",
	"/saas.accounts.v1.ModuleCapabilitiesService/NotifyUser",
	"/saas.accounts.v1.ModuleCapabilitiesService/PlaceRecord",
	"/saas.accounts.v1.ModuleCapabilitiesService/PublishEvent",
	"/saas.accounts.v1.ModuleCapabilitiesService/ReplayEvents",
	"/saas.accounts.v1.ModuleCapabilitiesService/RequestApproval",
	"/saas.accounts.v1.ModuleCapabilitiesService/ResolveWorkContextAuthority",
	"/saas.accounts.v1.ModuleCapabilitiesService/Subscribe",
	"/saas.accounts.v1.ModuleCapabilitiesService/Unsubscribe",
	"/saas.accounts.v1.WorkContextService/AuthorizeEvidenceRead",
	"/saas.accounts.v1.WorkContextService/CheckAuthorizationRevision",
}

var moduleAuthorityIndex = func() map[string]struct{} {
	index := make(map[string]struct{}, len(moduleAuthorityProcedures))
	for _, procedure := range moduleAuthorityProcedures {
		index[procedure] = struct{}{}
	}
	return index
}()

// ModuleAuthorityProcedures returns, sorted, the procedures served on
// ModuleAuthorityEndpoint.
func ModuleAuthorityProcedures() []string {
	out := append([]string(nil), moduleAuthorityProcedures...)
	sort.Strings(out)
	return out
}

// IsModuleAuthorityProcedure reports whether a composed module may call
// fullMethod on ModuleAuthorityEndpoint.
func IsModuleAuthorityProcedure(fullMethod string) bool {
	if !strings.HasPrefix(fullMethod, "/") {
		fullMethod = "/" + fullMethod
	}
	_, ok := moduleAuthorityIndex[fullMethod]
	return ok
}

// ModuleAuthorityAuthenticatesCaller reports whether fullMethod, served on
// ModuleAuthorityEndpoint, proves the calling module itself, so the endpoint
// admits it with no perimeter credential and the method's own refusal names
// what the caller failed to prove. The read-only oracles do not, and keep the
// perimeter credential.
func ModuleAuthorityAuthenticatesCaller(fullMethod string) bool {
	if !strings.HasPrefix(fullMethod, "/") {
		fullMethod = "/" + fullMethod
	}
	if _, oracle := moduleAuthorityReadOracles[fullMethod]; oracle {
		return false
	}
	return IsModuleAuthorityProcedure(fullMethod) && strings.HasPrefix(fullMethod, moduleAuthorityCapabilityService)
}

// ValidateModuleAuthorityProcedures holds the module surface to the internal
// tier. Every entry must
//
//  1. name a classified EXPOSURE_INTERNAL method, and
//  2. either belong to the capability surface that authenticates the calling
//     module from a signed Work Context — excluding its Mint* methods, which
//     authenticate with the module's secret and are the gateway's to broker —
//     or be one of the explicitly justified read-only oracles.
//
// It runs in the catalog and deployment generators, so a stale or widened entry
// fails generation rather than rendering a policy that admits nothing or too
// much. Rule 2 is what keeps a method that authorizes on the shared perimeter
// credential alone and mutates state from reaching every composed module by way
// of a one-line edit to the list above.
func ValidateModuleAuthorityProcedures() error {
	seen := make(map[string]bool, len(moduleAuthorityProcedures))
	for _, procedure := range moduleAuthorityProcedures {
		if seen[procedure] {
			return fmt.Errorf("module authority procedure %s is listed twice", procedure)
		}
		seen[procedure] = true
		policy, ok := LookupRPCPolicy(procedure)
		if !ok {
			return fmt.Errorf("module authority procedure %s is not a classified accounts method", procedure)
		}
		if policy.Tier != RPCPolicyInternal {
			return fmt.Errorf("module authority procedure %s is %s-tier; only internal-tier methods are served on the %q endpoint", procedure, policy.Tier, ModuleAuthorityEndpoint)
		}
		if _, oracle := moduleAuthorityReadOracles[procedure]; oracle {
			continue
		}
		if !strings.HasPrefix(procedure, moduleAuthorityCapabilityService) {
			return fmt.Errorf("module authority procedure %s neither authenticates the calling module from its Work Context (it is not on %s) nor is a declared read-only oracle; a method that authorizes on the shared perimeter credential alone must not be served on the %q endpoint", procedure, strings.TrimSuffix(strings.TrimPrefix(moduleAuthorityCapabilityService, "/"), "/"), ModuleAuthorityEndpoint)
		}
		if _, workloadMint := moduleAuthorityWorkloadMints[procedure]; strings.HasPrefix(policy.Method, "Mint") && !workloadMint {
			return fmt.Errorf("module authority procedure %s mints a module's own capability, which the authority interface's Issue serves; it must not be served on the %q endpoint", procedure, ModuleAuthorityEndpoint)
		}
	}
	for procedure := range moduleAuthorityWorkloadMints {
		if _, served := moduleAuthorityIndex[procedure]; !served {
			return fmt.Errorf("module authority workload mint %s is declared but not served; remove it rather than leaving a justification for a procedure nobody reaches", procedure)
		}
	}
	for procedure := range moduleAuthorityReadOracles {
		if _, served := moduleAuthorityIndex[procedure]; !served {
			return fmt.Errorf("module authority read oracle %s is declared but not served; remove it rather than leaving a justification for a procedure nobody reaches", procedure)
		}
	}
	return nil
}
