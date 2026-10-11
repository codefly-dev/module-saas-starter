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
// A READ FAILURE NEVER ADDS AUTHORITY, AND NEVER KEEPS ONE A REVOCATION
// REMOVED. Each pass reads applied presence and the delivery inbox in one
// snapshot, then derives withdrawals before verifying the newest documents.
// A failed snapshot publishes an empty degraded registry: partial reads cannot
// establish authority. If only the envelope cannot be read, the last registry
// is narrowed by the snapshot's withdrawals, presence tombstones and documents
// still in force under the current signer policy. Nothing is stamped with a
// revision the fence would accept for a newer ceiling.
//
// A principal the inputs describe incoherently — two live documents, a
// withdrawn binding, a document that no longer verifies, a signer the policy no
// longer admits, a workload the envelope does not allow, an entry that fails
// validation — is OMITTED by name and never widened. The host-wide inbox is why
// omission is per principal: one module's bad delivery must not take every
// other module off the host.
//
// GENERATIONS. The inbox keeps every generation and the desired set is the
// newest per authority id, so within an id a lower generation can never be the
// answer — that order is durable, in the store, on every replica. Across ids
// generations are not comparable, and a principal granted by two live
// documents is refused. A principal moving from one authority to another — X
// stops naming it, Y names it — is accepted only because Y is inside the
// current envelope: its units are filed under Y's presence binding, so the
// envelope admitting them is the administrator's approval of the move.

var (
	// ErrModulePrincipalInputsUnreadable reports that an input could not be
	// read, and that the registry published by the pass is degraded (see the
	// file comment) or, when the envelope itself could not be read, that the
	// published registry was not re-stamped.
	ErrModulePrincipalInputsUnreadable = errors.New("an input of the module registry could not be read")

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

	// ErrModuleWorkloadNotApproved reports a principal whose applied presence
	// runs its approved build under a workload the envelope does not allow.
	ErrModuleWorkloadNotApproved = errors.New("the presence names a workload the envelope does not allow for this principal")
)

// ModulePrincipalReconciler derives the module registry and installs it.
type ModulePrincipalReconciler struct {
	activation *SolutionAuthorityActivation

	// mutex serializes passes and guards last.
	mutex sync.Mutex
	// last is the registry this reconciler last published, with where each
	// entry came from. A degraded pass may only keep entries of it.
	last *publishedModuleRegistry
}

// publishedModuleRegistry is one published registry, the envelope revision it
// was derived from, and the source of each entry.
type publishedModuleRegistry struct {
	registry ModulePrincipalRegistry
	revision uint64
	sources  map[string]modulePrincipalSource
}

// modulePrincipalSource is what an entry was derived from: enough to re-judge
// it against a revocation without re-deriving it.
type modulePrincipalSource struct {
	presenceBinding string
	domain          string
	signer          string
	authority       string
	generation      uint64
}

// NewModulePrincipalReconciler builds the reconciler over the activation
// reader, which owns re-verification of every delivered authority document
// against the host's current signer policy and the read of the current ceiling.
func NewModulePrincipalReconciler(activation *SolutionAuthorityActivation) (*ModulePrincipalReconciler, error) {
	if activation == nil || activation.reconciler == nil {
		return nil, errors.New("the module principal reconciler requires the authority activation reader, which owns document re-verification and the ceiling")
	}
	return &ModulePrincipalReconciler{activation: activation}, nil
}

func (r *ModulePrincipalReconciler) publish(published *publishedModuleRegistry) {
	r.last = published
	r.activation.reconciler.service.installDerivedModulePrincipals(published.registry, published.revision)
}

// degraded is the last published registry kept only where it is still
// confirmed: derived from revision, over a binding neither withdrawn nor with
// its presence withdrawn, by a signer the host still admits for its domain —
// and, when this pass could read the newest documents, from a document still in
// force at the same generation (inForce: authority id → generation). A document
// this pass refused — it no longer verifies, its signer lost the domain, its
// binding was withdrawn — or superseded takes its cached entry with it. Never
// wider than the last published registry, by construction: it only removes
// entries.
func (r *ModulePrincipalReconciler) degraded(
	revision uint64, withdrawn map[string]string, withdrawnPresence map[string]bool, inForce map[string]uint64,
) *publishedModuleRegistry {
	kept := &publishedModuleRegistry{registry: ModulePrincipalRegistry{}, revision: revision, sources: map[string]modulePrincipalSource{}}
	if r.last == nil || r.last.revision != revision || withdrawn == nil || withdrawnPresence == nil {
		return kept
	}
	host := r.activation.reconciler.host
	for id, grant := range r.last.registry {
		source, known := r.last.sources[id]
		if !known {
			continue
		}
		if _, gone := withdrawn[source.presenceBinding]; gone || withdrawnPresence[source.presenceBinding] {
			continue
		}
		if !slices.Contains(host.Domains, source.domain) || !slices.Contains(host.DomainsBySigner[source.signer], source.domain) {
			continue
		}
		if inForce != nil {
			if generation, live := inForce[source.authority]; !live || generation != source.generation {
				continue
			}
		}
		kept.registry[id] = grant
		kept.sources[id] = source
	}
	return kept
}

// RunOnce derives the registry from the current inputs and installs it, tagged
// with the envelope revision it was derived from.
//
// It returns every refusal joined: a principal omitted, an operation audience
// omitted, a document outside the ceiling or refused, an input that could not
// be read.
func (r *ModulePrincipalReconciler) RunOnce(ctx context.Context) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// THE INPUTS, IN ONE SNAPSHOT, before anything else: the applied presence,
	// the authority history and the newest generations, read in one
	// repeatable-read transaction. The withdrawals, the documents in force and
	// the workloads are all derived from this one read, so a binding admitted
	// on another replica mid-pass is either wholly visible — owner, tombstone
	// and presence together — or not at all.
	inputs, inputsErr := r.activation.readRegistryInputs(ctx)
	if errors.Is(inputsErr, ErrSolutionAuthorityCeilingUnavailable) {
		// No delivery inbox wired: no document can ever be delivered, so no
		// principal can ever be asked for.
		envelope, err := r.activation.currentEnvelope(ctx)
		if err != nil {
			return fmt.Errorf("%w: the envelope: %w", ErrModulePrincipalInputsUnreadable, err)
		}
		var revision uint64
		if envelope != nil {
			revision = envelope.Revision
		}
		r.publish(&publishedModuleRegistry{registry: ModulePrincipalRegistry{}, revision: revision})
		return nil
	}
	var (
		withdrawn map[string]string
		presence  map[string]bool
		live      []liveVerifiedAuthority
		refusals  []error
		inForce   map[string]uint64
	)
	if inputsErr == nil {
		// THE REVOCATIONS FIRST, independently of document verification.
		withdrawn, inputsErr = r.activation.withdrawnIn(inputs)
	}
	if inputsErr == nil {
		presence, inputsErr = withdrawnPresenceIn(inputs)
	}
	if inputsErr == nil {
		live, refusals = r.activation.liveAuthorityIn(ctx, inputs, withdrawn)
		// The documents in force this pass: a cached entry never outlives one
		// this pass refused or superseded.
		inForce = make(map[string]uint64, len(live))
		for _, one := range live {
			inForce[one.document.Authority] = one.document.Generation
		}
	}

	envelope, envelopeErr := r.activation.currentEnvelope(ctx)
	if envelopeErr != nil {
		// The ceiling is unknown, so nothing is stamped with a new revision:
		// the last registry is narrowed by the revocations the snapshot could
		// establish and keeps its own revision, which the fence refuses once a
		// newer one is committed.
		if r.last != nil {
			if inputsErr != nil {
				withdrawn, presence, inForce = nil, nil, nil
			}
			r.publish(r.degraded(r.last.revision, withdrawn, presence, inForce))
		}
		return errors.Join(fmt.Errorf("%w: the envelope: %w", ErrModulePrincipalInputsUnreadable, envelopeErr), inputsErr)
	}
	if envelope == nil {
		// No ceiling has ever been written: nothing is allowed, so nothing is
		// granted. Not an unreadable input — the empty registry is the answer.
		r.publish(&publishedModuleRegistry{registry: ModulePrincipalRegistry{}})
		return nil
	}
	if inputsErr != nil {
		// No revocation can be established, so no entry can be kept.
		r.publish(r.degraded(envelope.Revision, nil, nil, nil))
		return fmt.Errorf("%w: %w", ErrModulePrincipalInputsUnreadable, inputsErr)
	}

	applied := make(map[string]*SolutionHostBindingRecord, len(inputs.applied))
	for _, record := range inputs.applied {
		applied[record.BindingID] = record
	}

	var inside []liveVerifiedAuthority
	for _, one := range live {
		document := one.document
		// THE CEILING, through core: the projection of the current envelope
		// for this document — the host's own units, whole.
		if err := document.ValidateAgainst(envelope.authorityEnvelopeFor(document)); err != nil {
			refusals = append(refusals, fmt.Errorf("%w: authority %q generation %d: %w",
				ErrModuleAuthorityOutsideEnvelope, document.Authority, document.Generation, err))
			continue
		}
		inside = append(inside, one)
	}

	// Which principals more than one document inside the ceiling grants to,
	// collected before anything is derived: the second document is what makes
	// the first ambiguous too.
	grantedBy := map[string][]string{}
	for _, one := range inside {
		for _, principal := range one.document.Principals {
			grantedBy[principal.Principal] = append(grantedBy[principal.Principal], one.document.Authority)
		}
	}

	next := &publishedModuleRegistry{registry: ModulePrincipalRegistry{}, revision: envelope.Revision, sources: map[string]modulePrincipalSource{}}
	for _, one := range inside {
		document := one.document
		candidates, workloadErr := r.workloadsFor(document, applied[document.PresenceBinding])
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
			workload, err := approvedWorkload(entry, candidates, document)
			if err != nil {
				omit(err)
				continue
			}
			grant, omitted, err := deriveModulePrincipalGrant(prefix, entry, document, principal.Bindings, workload)
			refusals = append(refusals, omitted...)
			if err != nil {
				omit(err)
				continue
			}
			id := ModulePrincipalID(prefix)
			next.registry[id] = grant
			next.sources[id] = modulePrincipalSource{presenceBinding: document.PresenceBinding, domain: document.OwnershipDomain,
				signer: one.signer, authority: document.Authority, generation: document.Generation}
		}
	}
	refusals = append(refusals, next.oneWorkloadOnePrincipal()...)

	r.publish(next)
	return errors.Join(refusals...)
}

// oneWorkloadOnePrincipal omits every principal whose workload (namespace and
// service account) another principal also derived. The envelope refuses such
// an overlap when written; this is the same rule at derivation, so a pod token
// can never authorize two prefixes whatever reached the registry.
func (p *publishedModuleRegistry) oneWorkloadOnePrincipal() []error {
	holders := map[ModuleWorkload][]string{}
	for id, grant := range p.registry {
		key := ModuleWorkload{Namespace: grant.Workload.Namespace, ServiceAccount: grant.Workload.ServiceAccount}
		holders[key] = append(holders[key], id)
	}
	var refusals []error
	for key, ids := range holders {
		if len(ids) < 2 {
			continue
		}
		var prefixes []string
		for _, id := range ids {
			prefixes = append(prefixes, p.registry[id].Prefix)
			delete(p.registry, id)
			delete(p.sources, id)
		}
		sort.Strings(prefixes)
		refusals = append(refusals, fmt.Errorf("%w: %v all run as workload %s/%s, and one workload maps to exactly one principal",
			ErrModulePrincipalOmitted, prefixes, key.Namespace, key.ServiceAccount))
	}
	return refusals
}

// workloadsFor reads the workloads a document's principals could run as, from
// the presence this host APPLIED for the binding the document is granted over:
// every distinct (SPIFFE ID, container) running the build the document is
// approved for.
//
// Applied, not merely delivered: a generation this host refused is not running
// here. The applied record holds the canonical document this host reconciled,
// written only by the reconcile pass. Which of these the principal may BE is
// the envelope's to say (approvedWorkload).
func (r *ModulePrincipalReconciler) workloadsFor(
	document *solutionhost.AuthorityDocument, record *SolutionHostBindingRecord,
) ([]ModuleWorkloadIdentity, error) {
	host := r.activation.reconciler.host
	if document.Host.Coordinate != host.Coordinate {
		return nil, fmt.Errorf("the document grants on host %q, and this host is %q", document.Host.Coordinate, host.Coordinate)
	}
	if record == nil || record.Applied == nil {
		return nil, fmt.Errorf("presence binding %q has no applied generation on this host", document.PresenceBinding)
	}
	if record.Applied.Removed {
		return nil, fmt.Errorf("presence binding %q is withdrawn on this host", document.PresenceBinding)
	}
	if record.Applied.Domain != document.OwnershipDomain {
		return nil, fmt.Errorf("presence binding %q was applied under ownership domain %q and the document speaks for %q",
			document.PresenceBinding, record.Applied.Domain, document.OwnershipDomain)
	}
	if record.Applied.Generation < document.EffectiveFrom {
		return nil, fmt.Errorf("the document is effective from presence generation %d and binding %q has applied %d",
			document.EffectiveFrom, document.PresenceBinding, record.Applied.Generation)
	}
	presence, err := solutionhost.Parse([]byte(record.Applied.Document))
	if err != nil {
		return nil, fmt.Errorf("read the applied presence for binding %q: %w", document.PresenceBinding, err)
	}
	if presence.Binding != document.PresenceBinding {
		return nil, fmt.Errorf("the applied presence for binding %q names binding %q", document.PresenceBinding, presence.Binding)
	}
	// Both halves of the tuple name the ceiling they were rendered against,
	// and they must agree — core's activation rule.
	if presence.EnvelopeRevision != document.EnvelopeRevision {
		return nil, fmt.Errorf("the applied presence for binding %q names envelope revision %d and the document names %d",
			document.PresenceBinding, presence.EnvelopeRevision, document.EnvelopeRevision)
	}
	var found []ModuleWorkloadIdentity
	for _, workload := range presence.Workloads {
		if workload.Image.Digest != document.ApprovedBuild {
			continue
		}
		candidate := ModuleWorkloadIdentity{SPIFFEID: workload.Identity.SPIFFEID, Container: workload.Container}
		if !slices.Contains(found, candidate) {
			found = append(found, candidate)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no workload of applied presence binding %q runs the approved build %s", document.PresenceBinding, document.ApprovedBuild)
	}
	return found, nil
}

// approvedWorkload is the one workload a principal runs as: the workload of the
// applied presence running its approved build, and only when the envelope
// allows that exact identity — SPIFFE ID and container.
//
// The envelope pins it because nothing else does. An image digest is not an
// identity: a later presence generation can run the same build under another
// service account, namespace or container, and binding the principal to
// whatever the newest presence names would move its authority to a workload
// nobody approved. Several allowed identities running the build have no single
// answer and are refused.
func approvedWorkload(
	entry ModuleEnvelopePrincipal, candidates []ModuleWorkloadIdentity, document *solutionhost.AuthorityDocument,
) (ModuleWorkload, error) {
	var allowed []ModuleWorkloadIdentity
	for _, candidate := range candidates {
		if slices.Contains(entry.WorkloadIdentities, candidate) {
			allowed = append(allowed, candidate)
		}
	}
	switch len(allowed) {
	case 0:
		return ModuleWorkload{}, fmt.Errorf("%w: binding %q runs build %s as %v",
			ErrModuleWorkloadNotApproved, document.PresenceBinding, document.ApprovedBuild, candidates)
	case 1:
		return allowed[0].workload(), nil
	}
	return ModuleWorkload{}, fmt.Errorf("%d allowed workloads of binding %q run build %s, so the principal's identity has no single answer",
		len(allowed), document.PresenceBinding, document.ApprovedBuild)
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
