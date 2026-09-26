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

// moduleAuthorityProcedures is the least-privilege subset of the internal tier
// a composed module may call on ModuleAuthorityEndpoint. Every entry is an
// EXPOSURE_INTERNAL method (the perimeter credential is verified on every
// call), and every entry additionally decides on a credential that names the
// caller or the capability being checked:
//
//   - the Work Context consumer seams take the capability under check in the
//     request and answer only about it;
//   - the module capability surface authenticates the calling module
//     principal from its verified Work Context and authorizes it against the
//     grant its composition declared (MODULE_PRINCIPALS).
//
// Deliberately absent, and reachable only on the private listener:
//
//   - the secret exchanges the auth-gateway brokers (module/solution
//     registration, module Work Context and operation-context mints), so a
//     module never presents its own secret to accounts directly;
//   - the registries and credential validators only the gateway reads
//     (solution and client registries, API-key validation, identity
//     resolution);
//   - the generic permission oracles and principal administration, which
//     would give a module an authority question about arbitrary principals
//     (AuthorizeEvidenceRead is the purpose-bound alternative);
//   - usage consumption and the headless installation mint, which today
//     authorize on the perimeter credential alone and so would let any module
//     act for any tenant.
//
// Adding a procedure here widens what every composed module can reach; the
// test beside this file holds each entry to the rules above.
var moduleAuthorityProcedures = []string{
	"/saas.accounts.v1.ModuleCapabilitiesService/AckJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/CancelApproval",
	"/saas.accounts.v1.ModuleCapabilitiesService/CheckWorkContextRecordAccess",
	"/saas.accounts.v1.ModuleCapabilitiesService/ClaimJobs",
	"/saas.accounts.v1.ModuleCapabilitiesService/EmitAuditEvent",
	"/saas.accounts.v1.ModuleCapabilitiesService/EnqueueJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/ExchangeDelegatedOperationAudience",
	"/saas.accounts.v1.ModuleCapabilitiesService/ExchangeDelegatedReadAudience",
	"/saas.accounts.v1.ModuleCapabilitiesService/FetchDatasourceBlob",
	"/saas.accounts.v1.ModuleCapabilitiesService/GetApproval",
	"/saas.accounts.v1.ModuleCapabilitiesService/HeartbeatJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/ListReadableSourceCollections",
	"/saas.accounts.v1.ModuleCapabilitiesService/ListSubjectVisibility",
	"/saas.accounts.v1.ModuleCapabilitiesService/ListSubscriptions",
	"/saas.accounts.v1.ModuleCapabilitiesService/NackJob",
	"/saas.accounts.v1.ModuleCapabilitiesService/NotifyUser",
	"/saas.accounts.v1.ModuleCapabilitiesService/PlaceRecord",
	"/saas.accounts.v1.ModuleCapabilitiesService/PublishEvent",
	"/saas.accounts.v1.ModuleCapabilitiesService/ReplayEvents",
	"/saas.accounts.v1.ModuleCapabilitiesService/RequestApproval",
	"/saas.accounts.v1.ModuleCapabilitiesService/Subscribe",
	"/saas.accounts.v1.ModuleCapabilitiesService/Unsubscribe",
	"/saas.accounts.v1.WorkContextService/AuthorizeEvidenceRead",
	"/saas.accounts.v1.WorkContextService/CheckAuthorizationRevision",
	"/saas.accounts.v1.WorkContextService/ConsumeSingleUse",
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

// ValidateModuleAuthorityProcedures holds the module surface to the internal
// tier: every entry must name a classified EXPOSURE_INTERNAL method. It runs in
// the catalog and deployment generators, so a stale or widened entry fails
// generation rather than rendering a policy that admits nothing or too much.
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
	}
	return nil
}
