package business

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
)

// ModuleAuthorityEnforcement is how the host answers a module whose applied
// manifest asks for authority the module envelope does not allow.
//
// Under `enforce` — the default — the registry is the intersection of what the
// manifest asks and what the envelope allows, and a binding, kind or principal
// the envelope withholds is withheld (ModuleBindingOmission, ErrModuleIdentityDenied).
//
// Under `warn` the host grants what the manifest asks and SAYS what it would
// have withheld, on every pass, in its log: an operator keeps the envelope's
// authorship while a module's contract moves, instead of every module caller
// hearing a refusal until the envelope is rewritten. It is a deployment's
// choice for a cell under validation, never a production default: a platform
// administrator writes `application/MODULE_AUTHORITY_ENFORCEMENT=warn`, and
// the boot log and every registry pass carry the banner. What stays enforced
// under warn: the token, the workload and the approved build a mint presents
// (that is authentication), the installation a capability names, and the
// tenant — a principal the envelope does not know is granted the tenant the
// envelope's other principals share, and refused when they share none.
type ModuleAuthorityEnforcement string

const (
	ModuleAuthorityEnforce ModuleAuthorityEnforcement = "enforce"
	ModuleAuthorityWarn    ModuleAuthorityEnforcement = "warn"
)

// ParseModuleAuthorityEnforcement reads the MODULE_AUTHORITY_ENFORCEMENT
// value. Empty keeps enforce; anything unrecognized is an error, so a typo
// cannot quietly turn enforcement off.
func ParseModuleAuthorityEnforcement(raw string) (ModuleAuthorityEnforcement, error) {
	switch mode := ModuleAuthorityEnforcement(strings.ToLower(strings.TrimSpace(raw))); mode {
	case "":
		return ModuleAuthorityEnforce, nil
	case ModuleAuthorityEnforce, ModuleAuthorityWarn:
		return mode, nil
	default:
		return "", fmt.Errorf("MODULE_AUTHORITY_ENFORCEMENT must be one of: enforce, warn (got %q)", raw)
	}
}

// SetModuleAuthorityEnforcement installs the deployment's enforcement mode.
// Warn is announced at the moment it is set, so the boot log carries it.
func (s *Service) SetModuleAuthorityEnforcement(mode ModuleAuthorityEnforcement) {
	s.moduleAuthorityEnforcement = mode
	if mode == ModuleAuthorityWarn {
		wool.Get(context.Background()).In("ModuleAuthorityEnforcement").Warn(
			"MODULE AUTHORITY ENFORCEMENT IS OFF (warn): every module is granted what its manifest asks for; what the envelope would withhold is logged, not withheld")
	}
}

// ModuleAuthorityEnforcementMode reports the installed mode, enforce by default.
func (s *Service) ModuleAuthorityEnforcementMode() ModuleAuthorityEnforcement {
	if s == nil || s.moduleAuthorityEnforcement == "" {
		return ModuleAuthorityEnforce
	}
	return s.moduleAuthorityEnforcement
}

// ErrModuleAuthorityTenantUnknown is a principal the envelope does not know,
// under warn, on an envelope whose principals share no single tenant: nothing
// can say which organization the module acts in, so it stays refused.
var ErrModuleAuthorityTenantUnknown = errors.New("the envelope declares no ceiling for it and its other principals share no single tenant, so even with enforcement off nothing names the organization it acts in")

// deriveUnderEnforcement is deriveModulePrincipalGrant under the deployment's
// enforcement mode. Under enforce it is that derivation. Under warn it runs it
// first to learn what would be withheld, says each withholding, and then
// grants what the manifest asks (requestedCeiling) against the same workloads.
func deriveUnderEnforcement(
	mode ModuleAuthorityEnforcement, prefix string, entry ModuleEnvelopePrincipal, allowed bool,
	manifest *solutionhost.ModuleManifest, workloads []ModuleWorkload, envelope *ModuleEnvelope,
) (ModulePrincipalGrant, []error, error) {
	if mode != ModuleAuthorityWarn {
		if !allowed {
			return ModulePrincipalGrant{}, nil, errors.New("the envelope declares no ceiling for it")
		}
		return deriveModulePrincipalGrant(prefix, entry, manifest, workloads)
	}
	log := wool.Get(context.Background()).In("ModuleAuthorityEnforcement")
	tenant, crossTenant := entry.Grant.Tenant, entry.Grant.CrossTenant
	if !allowed {
		shared, ok := sharedTenant(envelope)
		if !ok {
			return ModulePrincipalGrant{}, nil, ErrModuleAuthorityTenantUnknown
		}
		tenant = shared
		log.Warn("ENFORCEMENT OFF: the envelope declares no ceiling for this module; granting what its manifest asks, in the tenant the envelope's principals share",
			wool.Field("module", prefix), wool.Field("manifest", manifest.Binding), wool.Field("generation", manifest.Generation))
	} else {
		if _, omitted, err := deriveModulePrincipalGrant(prefix, entry, manifest, workloads); err != nil || len(omitted) > 0 {
			for _, omission := range omitted {
				log.Warn("ENFORCEMENT OFF: the envelope would withhold this; granted as the manifest asks",
					wool.Field("module", prefix), wool.Field("manifest", manifest.Binding), wool.Field("generation", manifest.Generation), wool.Field("withheld", omission.Error()))
			}
			if err != nil {
				log.Warn("ENFORCEMENT OFF: the envelope would refuse this module; granted as the manifest asks",
					wool.Field("module", prefix), wool.Field("manifest", manifest.Binding), wool.Field("generation", manifest.Generation), wool.Field("refusal", err.Error()))
			}
		}
	}
	ceiling := requestedCeiling(manifest.Authority, entry, tenant, crossTenant)
	return deriveModulePrincipalGrant(prefix, ceiling, manifest, workloads)
}

// sharedTenant is the one tenant every principal of the envelope names, when
// there is exactly one.
func sharedTenant(envelope *ModuleEnvelope) (string, bool) {
	if envelope == nil {
		return "", false
	}
	shared := ""
	for _, principal := range envelope.Principals {
		tenant := principal.Grant.Tenant
		if tenant == "" {
			continue
		}
		if shared != "" && shared != tenant {
			return "", false
		}
		shared = tenant
	}
	return shared, shared != ""
}

// requestedCeiling is the widest ceiling for a manifest: everything the entry
// the envelope holds already allows, widened to exactly what the authority
// block asks for (the same derivation an administrator's generator performs),
// with the tenancy given. Warn never grants less than the envelope does: a
// binding only the envelope names — a runtime's installation bindings, which
// no manifest can express — stays granted, and a binding both name is granted
// as the manifest asks. What the request cannot express — artifact policies
// and read audiences — is likewise kept from the held entry.
func requestedCeiling(request *solutionhost.Authority, held ModuleEnvelopePrincipal, tenant string, crossTenant bool) ModuleEnvelopePrincipal {
	grant := ModulePrincipalGrant{
		ArtifactPolicies: held.Grant.ArtifactPolicies,
		ReadAudiences:    held.Grant.ReadAudiences,
		Tenant:           tenant,
		CrossTenant:      crossTenant,
		Queues:           sortedCopy(held.Grant.Queues),
		Namespaces:       sortedCopy(held.Grant.Namespaces),
		Resources:        sortedCopy(held.Grant.Resources),
	}
	if len(held.Grant.OperationAudiences) > 0 {
		grant.OperationAudiences = maps.Clone(held.Grant.OperationAudiences)
	}
	if request == nil {
		return ModuleEnvelopePrincipal{Grant: grant}
	}
	grant.Queues = sortedUnion(held.Grant.Queues, request.Queues)
	grant.Namespaces = sortedUnion(held.Grant.Namespaces, request.Namespaces)
	grant.Resources = sortedUnion(held.Grant.Resources, request.ResourceKinds)
	// A connect names one binding per module, so exactly one may accept
	// delegations: the one the envelope already chose, or the only binding
	// when the manifest declares one. With several and no choice, none does —
	// a manifest cannot say which, and the host does not guess.
	delegating := ""
	for id, audience := range held.Grant.OperationAudiences {
		if audience.SourceDelegationScopes != nil {
			delegating = id
		}
	}
	if delegating == "" && len(request.Bindings) == 1 {
		delegating = request.Bindings[0].ID
	}
	for _, binding := range request.Bindings {
		if binding.ID == "" || binding.Audience == "" {
			continue
		}
		invoke := scopesOf(binding.Operations[modulecontract.OperationInvoke])
		audience := ModuleOperationAudience{
			Audience:       binding.Audience,
			InvokeScopes:   invoke,
			LookupScopes:   scopesOf(binding.Operations[modulecontract.OperationLookup]),
			HeadlessScopes: nilIfEmpty(scopesOf(binding.Operations[modulecontract.OperationHeadless])),
		}
		if binding.ID == delegating {
			audience.SourceDelegationScopes = nilIfEmpty(invoke)
		}
		if grant.OperationAudiences == nil {
			grant.OperationAudiences = map[string]ModuleOperationAudience{}
		}
		grant.OperationAudiences[binding.ID] = audience
	}
	return ModuleEnvelopePrincipal{Grant: grant}
}

// scopesOf groups "kind:action" unit scopes by kind, actions sorted, no
// resource ids: a manifest names kinds, never instances.
func scopesOf(units []string) []ModuleOperationScope {
	byKind := map[string]map[string]bool{}
	for _, unit := range units {
		kind, action, ok := strings.Cut(unit, ":")
		if !ok || kind == "" || action == "" {
			continue
		}
		if byKind[kind] == nil {
			byKind[kind] = map[string]bool{}
		}
		byKind[kind][action] = true
	}
	kinds := make([]string, 0, len(byKind))
	for kind := range byKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	scopes := make([]ModuleOperationScope, 0, len(kinds))
	for _, kind := range kinds {
		actions := make([]string, 0, len(byKind[kind]))
		for action := range byKind[kind] {
			actions = append(actions, action)
		}
		sort.Strings(actions)
		scopes = append(scopes, ModuleOperationScope{ResourceKind: kind, Actions: actions})
	}
	return scopes
}

// sortedUnion is every distinct value of both lists, sorted; nil when both
// are empty, like sortedCopy.
func sortedUnion(left, right []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, values := range [][]string{left, right} {
		for _, value := range values {
			if seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func sortedCopy(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
