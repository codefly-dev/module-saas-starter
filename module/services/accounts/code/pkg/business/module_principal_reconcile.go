package business

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
)

// The module registry, DERIVED (the solution lifecycle design, §3 and §4.2).
//
// WHAT THIS REPLACES. Every capability decision reads one registry —
// declaredModules() — and that registry was MODULE_PRINCIPALS, parsed once from
// the environment at boot. Nothing wrote it afterwards, so adding a module,
// narrowing one, or withdrawing one meant a configuration edit and a restart,
// and a delivered authority document reached nothing any decision read.
//
// WHAT IT IS NOW. Each reconcile pass builds the WHOLE registry from three
// inputs and swaps it in with SetModulePrincipals:
//
//   - the current envelope, which a platform super_admin writes at runtime;
//   - every live, re-verified authority document inside that envelope;
//   - the applied presence document each authority is granted over.
//
// A principal is in the registry only while all three agree: the envelope
// allows it, a signed delivery asks for it, and the presence it names is
// applied on this host. Its entry is the envelope's grant intersected with what
// the document asks for — never wider than either.
//
// THE PREVIOUS REGISTRY SURVIVES ONLY AN UNREADABLE INPUT. If the envelope, the
// inbox or the applied state cannot be read, nothing was judged and the last
// registry keeps serving: reading a failed read as "no modules" would withdraw
// every module on a transient error. Every other outcome is a fresh registry,
// and a principal the inputs describe incoherently — two live documents, a
// rewound generation, a workload that cannot be resolved, an entry that fails
// validation — is OMITTED by name and never widened. The host-wide inbox is why
// omission is per principal: one module's bad delivery must not take every
// other module off the host.

var (
	// ErrModulePrincipalInputsUnreadable reports that the envelope, the
	// authority inbox or the applied presence could not be read, so the
	// registry was not replaced.
	ErrModulePrincipalInputsUnreadable = errors.New("the module registry's inputs could not be established, so the registry is unchanged")

	// ErrModulePrincipalOmitted reports a principal left out of the registry
	// this pass, with the reason beside it.
	ErrModulePrincipalOmitted = errors.New("module principal omitted from the registry")

	// ErrModulePrincipalBindingOmitted reports an operation audience left out of
	// a principal's entry because what the document grants and what the
	// envelope allows have nothing in common.
	ErrModulePrincipalBindingOmitted = errors.New("module operation audience omitted from the registry")

	// ErrModuleAuthorityOutsideEnvelope reports a live authority document the
	// current envelope does not hold. It grants nothing.
	ErrModuleAuthorityOutsideEnvelope = errors.New("delivered authority is outside the current envelope")
)

// ModulePrincipalReconciler derives the module registry and installs it.
type ModulePrincipalReconciler struct {
	activation *SolutionAuthorityActivation

	// mutex serializes passes. The registry swap and the high-water marks are
	// one decision, so two passes interleaving could install one pass's
	// registry with the other's marks.
	mutex sync.Mutex
	// marks is the highest authority generation this host has served per
	// principal. It only grows, and it outlives each registry for the reason
	// the approved-build view's does: a later pass carrying a lower generation
	// is a rewind to refuse, not a fresh start.
	marks map[string]uint64
}

// NewModulePrincipalReconciler builds the reconciler over the activation
// reader, which owns re-verification of every delivered authority document
// against the host's current signer policy and the read of the current ceiling.
func NewModulePrincipalReconciler(activation *SolutionAuthorityActivation) (*ModulePrincipalReconciler, error) {
	if activation == nil || activation.reconciler == nil {
		return nil, errors.New("the module principal reconciler requires the authority activation reader, which owns document re-verification and the ceiling")
	}
	return &ModulePrincipalReconciler{activation: activation, marks: map[string]uint64{}}, nil
}

// RunOnce derives the registry from the current inputs and installs it.
//
// It returns every refusal joined: a principal omitted, an operation audience
// omitted, a document outside the ceiling. A non-nil return with the registry
// replaced is the normal shape of a host that holds one bad delivery; only
// ErrModulePrincipalInputsUnreadable means nothing was installed.
func (r *ModulePrincipalReconciler) RunOnce(ctx context.Context) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	service := r.activation.reconciler.service

	envelope, err := r.activation.currentEnvelope(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrModulePrincipalInputsUnreadable, err)
	}
	if envelope == nil {
		// No ceiling has ever been written: nothing is allowed, so nothing is
		// granted. Not an unreadable input — the empty registry is the answer.
		service.SetModulePrincipals(ModulePrincipalRegistry{})
		return nil
	}
	read, err := r.activation.verifiedAuthority(ctx)
	switch {
	case errors.Is(err, ErrSolutionAuthorityCeilingUnavailable):
		// No delivery inbox wired: no document can ever be delivered, so no
		// principal can ever be asked for.
		service.SetModulePrincipals(ModulePrincipalRegistry{})
		return nil
	case err != nil:
		return fmt.Errorf("%w: %w", ErrModulePrincipalInputsUnreadable, err)
	}
	records, err := service.ListSolutionHostBindings(ctx)
	if err != nil {
		return fmt.Errorf("%w: read applied presence: %w", ErrModulePrincipalInputsUnreadable, err)
	}
	applied := make(map[string]*SolutionHostBindingRecord, len(records))
	for _, record := range records {
		applied[record.BindingID] = record
	}

	var refusals []error
	// A withdrawal is terminal for the presence binding it is over, whatever
	// authority id a later grant is signed under — the rule activation holds,
	// held here too, so a rename cannot reinstate what a tombstone withdrew.
	withdrawn := map[string]string{}
	for _, one := range read {
		if one.document.Removed {
			withdrawn[one.document.PresenceBinding] = one.document.Authority
		}
	}
	var inside []*solutionhost.AuthorityDocument
	for _, one := range read {
		document := one.document
		if document.Removed {
			continue
		}
		if authority, gone := withdrawn[document.PresenceBinding]; gone && authority != document.Authority {
			refusals = append(refusals, fmt.Errorf("%w: authority %q over binding %q, which authority %q withdrew",
				ErrSolutionAuthorityWithdrawn, document.Authority, document.PresenceBinding, authority))
			continue
		}
		// THE CEILING, through core: the projection of the current envelope for
		// this document, held by core's own containment rule.
		if err := document.ValidateAgainst(envelope.authorityEnvelopeFor(document)); err != nil {
			refusals = append(refusals, fmt.Errorf("%w: authority %q generation %d: %w",
				ErrModuleAuthorityOutsideEnvelope, document.Authority, document.Generation, err))
			continue
		}
		inside = append(inside, document)
	}

	// Which principals more than one document inside the ceiling grants to,
	// collected before anything is derived: the second document is what makes
	// the first ambiguous too.
	grantedBy := map[string][]string{}
	for _, document := range inside {
		for _, principal := range document.Principals {
			grantedBy[principal.Principal] = append(grantedBy[principal.Principal], document.Authority)
		}
	}

	marks := maps.Clone(r.marks)
	next := ModulePrincipalRegistry{}
	for _, document := range inside {
		workload, workloadErr := r.workloadFor(document, applied[document.PresenceBinding])
		for _, principal := range document.Principals {
			prefix := principal.Principal
			omit := func(reason error) {
				refusals = append(refusals, fmt.Errorf("%w: %q (authority %q generation %d): %w",
					ErrModulePrincipalOmitted, prefix, document.Authority, document.Generation, reason))
			}
			if authorities := grantedBy[prefix]; len(authorities) > 1 {
				named := slices.Clone(authorities)
				sort.Strings(named)
				omit(fmt.Errorf("granted by authorities %v, whose generations are not comparable", named))
				continue
			}
			if mark, had := marks[prefix]; had && document.Generation < mark {
				omit(fmt.Errorf("generation %d is below the %d already served", document.Generation, mark))
				continue
			}
			entry, declared := envelope.Principals[prefix]
			if !declared {
				// Unreachable through ValidateAgainst — a principal with no
				// entry has no unit admitted — and refused rather than trusted
				// to stay unreachable.
				omit(errors.New("the envelope declares no ceiling for it"))
				continue
			}
			if workloadErr != nil {
				omit(workloadErr)
				continue
			}
			grant, omitted, err := deriveModulePrincipalGrant(prefix, entry, document, principal.Bindings, workload)
			refusals = append(refusals, omitted...)
			if err != nil {
				omit(err)
				continue
			}
			next[ModulePrincipalID(prefix)] = grant
			marks[prefix] = document.Generation
		}
	}

	service.SetModulePrincipals(next)
	r.marks = marks
	return errors.Join(refusals...)
}

// workloadFor resolves the Kubernetes identity a document's principals run as,
// from the presence this host APPLIED for the binding the document is granted
// over.
//
// Applied, not merely delivered: a generation this host refused is not running
// here, and a workload read from it would bind a principal to pods nothing
// admitted. The applied record holds the canonical document this host
// reconciled, written only by the reconcile pass.
//
// The authenticating workload is the one running the build the document is
// approved for — delivery approves the image digest of the container that
// presents the module's identity — and its namespace and service account are
// read from the SPIFFE ID the presence document names for it. More than one
// distinct identity running that build has no single answer and is refused.
func (r *ModulePrincipalReconciler) workloadFor(
	document *solutionhost.AuthorityDocument, record *SolutionHostBindingRecord,
) (ModuleWorkload, error) {
	host := r.activation.reconciler.host
	if document.Host.Coordinate != host.Coordinate {
		return ModuleWorkload{}, fmt.Errorf("the document grants on host %q, and this host is %q", document.Host.Coordinate, host.Coordinate)
	}
	if record == nil || record.Applied == nil {
		return ModuleWorkload{}, fmt.Errorf("presence binding %q has no applied generation on this host", document.PresenceBinding)
	}
	if record.Applied.Removed {
		return ModuleWorkload{}, fmt.Errorf("presence binding %q is withdrawn on this host", document.PresenceBinding)
	}
	if record.Applied.Domain != document.OwnershipDomain {
		return ModuleWorkload{}, fmt.Errorf("presence binding %q was applied under ownership domain %q and the document speaks for %q",
			document.PresenceBinding, record.Applied.Domain, document.OwnershipDomain)
	}
	if record.Applied.Generation < document.EffectiveFrom {
		return ModuleWorkload{}, fmt.Errorf("the document is effective from presence generation %d and binding %q has applied %d",
			document.EffectiveFrom, document.PresenceBinding, record.Applied.Generation)
	}
	presence, err := solutionhost.Parse([]byte(record.Applied.Document))
	if err != nil {
		return ModuleWorkload{}, fmt.Errorf("read the applied presence for binding %q: %w", document.PresenceBinding, err)
	}
	if presence.Binding != document.PresenceBinding {
		return ModuleWorkload{}, fmt.Errorf("the applied presence for binding %q names binding %q", document.PresenceBinding, presence.Binding)
	}
	// Both halves of the tuple name the ceiling they were rendered against,
	// and they must agree — core's activation rule — so authority rendered
	// against one envelope revision never binds to presence rendered against
	// another.
	if presence.EnvelopeRevision != document.EnvelopeRevision {
		return ModuleWorkload{}, fmt.Errorf("the applied presence for binding %q names envelope revision %d and the document names %d",
			document.PresenceBinding, presence.EnvelopeRevision, document.EnvelopeRevision)
	}
	var found []ModuleWorkload
	for _, workload := range presence.Workloads {
		if workload.Image.Digest != document.ApprovedBuild {
			continue
		}
		candidate := ModuleWorkload{
			ServiceAccount: spiffeServiceAccount(workload.Identity.SPIFFEID),
			Namespace:      spiffeNamespace(workload.Identity.SPIFFEID),
			Container:      workload.Container,
		}
		if !slices.Contains(found, candidate) {
			found = append(found, candidate)
		}
	}
	switch len(found) {
	case 0:
		return ModuleWorkload{}, fmt.Errorf("no workload of applied presence binding %q runs the approved build %s", document.PresenceBinding, document.ApprovedBuild)
	case 1:
		return found[0], nil
	}
	return ModuleWorkload{}, fmt.Errorf("%d distinct workloads of applied presence binding %q run the approved build %s, so the principal's identity has no single answer",
		len(found), document.PresenceBinding, document.ApprovedBuild)
}

// spiffeServiceAccount reads the service account out of
// `spiffe://<trust domain>/ns/<ns>/sa/<account>`, empty when the shape is not
// that — never a guess, for the reason spiffeNamespace gives.
func spiffeServiceAccount(id string) string {
	_, path, found := strings.Cut(id, "://")
	if !found {
		return ""
	}
	segments := strings.Split(path, "/")
	for index := 0; index+1 < len(segments); index++ {
		if segments[index] == "sa" {
			return segments[index+1]
		}
	}
	return ""
}

// deriveModulePrincipalGrant is one registry entry: the envelope's grant for the
// principal, bounded by what the document asks for.
//
//   - queues, namespaces: the envelope's, kept only where the document declares
//     them too (core already refused a document declaring more);
//   - resources: the envelope's, kept only where the document's scope ceilings
//     name the kind — the module's own vocabulary;
//   - external namespaces: the envelope's, within the namespaces kept;
//   - operation audiences: the envelope's, kept only for a binding the document
//     grants an invoke unit for, each operation's scopes intersected with the
//     scope of the unit the document grants for it; an operation the document
//     grants no unit for is absent;
//   - tenant, cross_tenant, read audiences and artifact policies: the
//     envelope's, which no delivered document may speak to;
//   - workload: the applied presence's.
//
// The result is validated whole; an entry that fails is an error and the
// caller omits the principal.
func deriveModulePrincipalGrant(
	prefix string, entry ModuleEnvelopePrincipal, document *solutionhost.AuthorityDocument,
	units []solutionhost.AuthorityBinding, workload ModuleWorkload,
) (ModulePrincipalGrant, []error, error) {
	ceiling := entry.Grant
	grant := ModulePrincipalGrant{
		ArtifactPolicies: maps.Clone(ceiling.ArtifactPolicies),
		ReadAudiences:    maps.Clone(ceiling.ReadAudiences),
		Prefix:           prefix,
		Queues:           keepDeclared(ceiling.Queues, document.Queues),
		Namespaces:       keepDeclared(ceiling.Namespaces, document.Namespaces),
		CrossTenant:      ceiling.CrossTenant,
		Tenant:           ceiling.Tenant,
		Workload:         workload,
	}
	grant.ExternalNamespaces = keepDeclared(ceiling.ExternalNamespaces, grant.Namespaces)
	kinds := make([]string, 0, len(document.ScopeCeilings))
	for _, declared := range document.ScopeCeilings {
		kinds = append(kinds, declared.ResourceKind)
	}
	grant.Resources = keepDeclared(ceiling.Resources, kinds)

	byID := make(map[string]solutionhost.AuthorityBinding, len(units))
	for _, unit := range units {
		byID[unit.ID] = unit
	}
	unitFor := func(binding, operation string) (solutionhost.AuthorityBinding, bool) {
		unit, ok := byID[document.PresenceBinding+":"+binding+":"+operation]
		return unit, ok
	}
	var omitted []error
	ids := slices.Sorted(maps.Keys(ceiling.OperationAudiences))
	for _, id := range ids {
		allowed := ceiling.OperationAudiences[id]
		invoke, granted := unitFor(id, modulecontract.OperationInvoke)
		if !granted {
			// The module did not ask for this audience. Not a refusal: the
			// envelope allows more than one delivery needs.
			continue
		}
		invokeScopes := scopeSet(invoke.Scope)
		derived := ModuleOperationAudience{
			Audience:     allowed.Audience,
			InvokeScopes: intersectUnitScopes(allowed.InvokeScopes, invokeScopes),
		}
		if lookup, ok := unitFor(id, modulecontract.OperationLookup); ok {
			derived.LookupScopes = intersectUnitScopes(allowed.LookupScopes, scopeSet(lookup.Scope))
		}
		if headless, ok := unitFor(id, modulecontract.OperationHeadless); ok && allowed.HeadlessScopes != nil {
			derived.HeadlessScopes = nilIfEmpty(intersectUnitScopes(allowed.HeadlessScopes, scopeSet(headless.Scope)))
		}
		// Source delegation acts on a person's behalf, so it is bounded by the
		// invoke grant the document gives — never by nothing.
		if allowed.SourceDelegationScopes != nil {
			derived.SourceDelegationScopes = nilIfEmpty(intersectUnitScopes(allowed.SourceDelegationScopes, invokeScopes))
		}
		if err := validateOperationAudiences(prefix, map[string]ModuleOperationAudience{id: derived}); err != nil {
			omitted = append(omitted, fmt.Errorf("%w: %q binding %q: what authority %q grants and what the envelope allows leave no valid audience: %w",
				ErrModulePrincipalBindingOmitted, prefix, id, document.Authority, err))
			continue
		}
		if grant.OperationAudiences == nil {
			grant.OperationAudiences = map[string]ModuleOperationAudience{}
		}
		grant.OperationAudiences[id] = derived
	}
	if err := ValidateModulePrincipalGrant(prefix, grant); err != nil {
		return ModulePrincipalGrant{}, omitted, err
	}
	return grant, omitted, nil
}

// keepDeclared is allowed restricted to declared, in allowed's order. Never nil
// for a non-empty result and nil for an empty one, so an entry's revision hash
// does not change with how an empty list was spelled.
func keepDeclared(allowed, declared []string) []string {
	var kept []string
	for _, value := range allowed {
		if slices.Contains(declared, value) {
			kept = append(kept, value)
		}
	}
	return kept
}

// scopeSet reads a unit's scope — sorted "kind:action" values joined by commas,
// as delivery writes it — into a set.
func scopeSet(scope string) map[string]bool {
	set := map[string]bool{}
	for _, value := range strings.Split(scope, ",") {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	return set
}

// intersectUnitScopes keeps, of each allowed scope, the actions the unit's
// scope grants on the same kind. A scope left with no action is dropped. Resource
// IDs stay the envelope's: a unit cannot name them, so it can never widen them.
func intersectUnitScopes(allowed []ModuleOperationScope, granted map[string]bool) []ModuleOperationScope {
	var kept []ModuleOperationScope
	for _, scope := range allowed {
		var actions []string
		for _, action := range scope.Actions {
			if granted[scope.ResourceKind+":"+action] {
				actions = append(actions, action)
			}
		}
		if len(actions) == 0 {
			continue
		}
		kept = append(kept, ModuleOperationScope{
			ResourceKind: scope.ResourceKind,
			Actions:      actions,
			ResourceIDs:  slices.Clone(scope.ResourceIDs),
		})
	}
	return kept
}

func nilIfEmpty(scopes []ModuleOperationScope) []ModuleOperationScope {
	if len(scopes) == 0 {
		return nil
	}
	return scopes
}
