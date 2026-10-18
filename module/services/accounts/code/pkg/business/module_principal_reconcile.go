package business

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"

	modulecontract "github.com/codefly-dev/core/contracts/module"
	"github.com/codefly-dev/core/solutionhost"
)

// The module registry, DERIVED (the solution lifecycle design, §3 and §4.2).
//
// Every capability decision reads one registry — declaredModules() — and each
// reconcile pass builds the WHOLE of it from two inputs and swaps it in:
//
//   - the module manifests this host has APPLIED, each re-verified against the
//     current pins: which workloads run as the module, and the authority it
//     asks for;
//   - the current envelope, the administrator's approval, which a platform
//     super_admin writes at runtime.
//
// A principal is in the registry only while both agree: an applied, live
// manifest asks for it, and the envelope allows it. Its entry is the
// envelope's grant intersected with what the manifest asks for — never wider
// than either — and its workloads are the manifest's, spelled by the cell's
// rule (namespace from the module, ServiceAccount from the service).
//
// A READ FAILURE NEVER ADDS AUTHORITY. A pass that cannot read the applied
// manifests publishes an empty registry. A pass that can read them but not the
// envelope keeps only the entries whose manifest is still the applied, live
// generation it was derived from, at the revision it was derived under, so the
// envelope fence still refuses them once a newer ceiling is committed.
//
// A principal the inputs describe incoherently — two manifests asking for it,
// a manifest that no longer verifies, a workload two principals claim, an
// entry that fails validation — is OMITTED by name and never widened. One
// module's bad delivery must not take every other module off the host.

var (
	// ErrModulePrincipalInputsUnreadable reports that an input could not be
	// read, and that the published registry is degraded (see above).
	ErrModulePrincipalInputsUnreadable = errors.New("an input of the module registry could not be read")

	// ErrModulePrincipalOmitted reports a principal left out of the registry
	// this pass, with the reason beside it.
	ErrModulePrincipalOmitted = errors.New("module principal omitted from the registry")

	// ErrModulePrincipalBindingOmitted reports an operation binding a manifest
	// asks for that the envelope does not allow, or allows nothing of.
	ErrModulePrincipalBindingOmitted = errors.New("module operation audience omitted from the registry")
)

// ModulePrincipalReconciler derives the module registry each pass.
type ModulePrincipalReconciler struct {
	reconciler *SolutionHostBindingReconciler
	envelopes  ModuleEnvelopeSource

	mutex sync.Mutex
	last  *publishedModuleRegistry
}

// publishedModuleRegistry is what a pass installed, and what each entry was
// derived from, so a degraded pass can tell which entries still stand.
type publishedModuleRegistry struct {
	registry ModulePrincipalRegistry
	revision uint64
	sources  map[string]modulePrincipalSource
	// delivered is the inbox's size when the inputs were read: the marker a
	// mint compares to learn a delivery has not been read yet.
	delivered *uint64
}

// modulePrincipalSource is the applied generation an entry was derived from.
type modulePrincipalSource struct {
	binding    string
	generation uint64
}

func (r *ModulePrincipalReconciler) publish(published *publishedModuleRegistry) {
	r.last = published
	r.reconciler.service.InstallDerivedModulePrincipals(published.registry, published.revision, published.delivered)
}

// modulePrincipalInputs is one snapshot of the applied state and the inbox.
type modulePrincipalInputs struct {
	applied   []*SolutionHostBindingRecord
	delivered *uint64
}

func (r *ModulePrincipalReconciler) readInputs(ctx context.Context) (*modulePrincipalInputs, error) {
	service := r.reconciler.service
	inputs := &modulePrincipalInputs{}
	err := service.store.WithControlPlaneReadSnapshot(ctx, func(ctx context.Context) error {
		var err error
		if inputs.applied, err = service.store.ListSolutionHostBindings(ctx); err != nil {
			return fmt.Errorf("the applied manifests: %w", err)
		}
		if counter, counts := service.deliveryStore.(SolutionDeliveryCounter); counts {
			delivered, err := counter.CountDeliveredDocuments(ctx)
			if err != nil {
				return fmt.Errorf("the inbox's size: %w", err)
			}
			inputs.delivered = &delivered
		}
		return nil
	})
	return inputs, err
}

func (r *ModulePrincipalReconciler) currentEnvelope(ctx context.Context) (*ModuleEnvelope, error) {
	if r.envelopes == nil {
		return nil, nil
	}
	envelope, err := r.envelopes.CurrentModuleEnvelope(ctx)
	if errors.Is(err, ErrModuleEnvelopeUnavailable) {
		return nil, nil
	}
	return envelope, err
}

// degraded keeps the entries of the last registry whose manifest is still the
// applied, live generation they were derived from.
func (r *ModulePrincipalReconciler) degraded(applied []*SolutionHostBindingRecord) *publishedModuleRegistry {
	kept := &publishedModuleRegistry{registry: ModulePrincipalRegistry{}, sources: map[string]modulePrincipalSource{}}
	if r.last == nil {
		return kept
	}
	kept.revision = r.last.revision
	live := map[string]uint64{}
	for _, record := range applied {
		if record.Applied != nil && !record.Applied.Removed {
			live[record.BindingID] = record.Applied.Generation
		}
	}
	for id, grant := range r.last.registry {
		source := r.last.sources[id]
		if generation, ok := live[source.binding]; ok && generation == source.generation {
			kept.registry[id] = grant
			kept.sources[id] = source
		}
	}
	return kept
}

// RunOnce derives the registry from the applied manifests and the envelope and
// installs it. Its error names every principal and binding it omitted.
func (r *ModulePrincipalReconciler) RunOnce(ctx context.Context) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	inputs, inputsErr := r.readInputs(ctx)
	if inputsErr != nil {
		r.publish(&publishedModuleRegistry{registry: ModulePrincipalRegistry{}, revision: r.lastRevision()})
		return fmt.Errorf("%w: %w", ErrModulePrincipalInputsUnreadable, inputsErr)
	}
	envelope, err := r.currentEnvelope(ctx)
	if err != nil {
		r.publish(r.degraded(inputs.applied))
		return fmt.Errorf("%w: the envelope: %w", ErrModulePrincipalInputsUnreadable, err)
	}
	if envelope == nil {
		r.publish(&publishedModuleRegistry{registry: ModulePrincipalRegistry{}, delivered: inputs.delivered})
		return nil
	}

	manifests, refusals := r.appliedManifestsVerified(ctx, inputs.applied)
	askedBy := map[string][]string{}
	for _, manifest := range manifests {
		if manifest.Authority != nil {
			askedBy[manifest.Authority.Principal] = append(askedBy[manifest.Authority.Principal], manifest.Binding)
		}
	}

	next := &publishedModuleRegistry{
		registry: ModulePrincipalRegistry{}, revision: envelope.Revision,
		sources: map[string]modulePrincipalSource{}, delivered: inputs.delivered,
	}
	rule := r.reconciler.service.moduleNamespaces
	for _, manifest := range manifests {
		if manifest.Authority == nil {
			// A manifest with no authority asks for nothing: a module that is
			// present and holds no principal.
			continue
		}
		prefix := manifest.Authority.Principal
		omit := func(reason error) {
			refusals = append(refusals, fmt.Errorf("%w: %q (binding %q generation %d): %w",
				ErrModulePrincipalOmitted, prefix, manifest.Binding, manifest.Generation, reason))
		}
		if bindings := askedBy[prefix]; len(bindings) > 1 {
			named := slices.Clone(bindings)
			sort.Strings(named)
			omit(fmt.Errorf("asked for by the manifests of bindings %v, so which one it runs as has no single answer", named))
			continue
		}
		entry, allowed := envelope.Principals[prefix]
		if !allowed {
			omit(errors.New("the envelope declares no ceiling for it"))
			continue
		}
		workloads := make([]ModuleWorkload, 0, len(manifest.Workloads))
		for _, workload := range manifest.Workloads {
			workloads = append(workloads, ModuleWorkload{
				Namespace: rule.Namespace(manifest.Alias), ServiceAccount: workload.Service, Container: workload.Container,
			})
		}
		grant, omitted, err := deriveModulePrincipalGrant(prefix, entry, manifest, workloads)
		refusals = append(refusals, omitted...)
		if err != nil {
			omit(err)
			continue
		}
		id := ModulePrincipalID(prefix)
		next.registry[id] = grant
		next.sources[id] = modulePrincipalSource{binding: manifest.Binding, generation: manifest.Generation}
	}
	refusals = append(refusals, next.oneWorkloadOnePrincipal()...)

	r.publish(next)
	return errors.Join(refusals...)
}

func (r *ModulePrincipalReconciler) lastRevision() uint64 {
	if r.last == nil {
		return 0
	}
	return r.last.revision
}

// appliedManifestsVerified is every applied, live manifest asking for authority
// whose stored carrier still verifies under the current pins and is exactly the
// applied generation.
// Re-verifying is what makes an unpinned key stop granting: its manifests stay
// applied (a removal is a withdrawal generation, never a lost pin) but they
// confer no authority.
func (r *ModulePrincipalReconciler) appliedManifestsVerified(
	ctx context.Context, records []*SolutionHostBindingRecord,
) ([]*solutionhost.ModuleManifest, []error) {
	service := r.reconciler.service
	var (
		manifests []*solutionhost.ModuleManifest
		refusals  []error
	)
	for _, record := range records {
		if record.Applied == nil || record.Applied.Removed {
			continue
		}
		refuse := func(err error) {
			refusals = append(refusals, fmt.Errorf("%w: binding %q generation %d grants nothing: %w",
				ErrModulePrincipalOmitted, record.BindingID, record.Applied.Generation, err))
		}
		manifest, err := record.AppliedManifest()
		if err != nil {
			refuse(err)
			continue
		}
		if manifest.Authority == nil {
			// Asks for nothing, so there is nothing to re-verify it for.
			continue
		}
		if service.deliveryStore == nil {
			refuse(errors.New("this host has no delivery inbox to re-verify the applied manifest against"))
			continue
		}
		var stored *SolutionDeliveryRecord
		if err := service.store.WithControlPlane(ctx, func(ctx context.Context) error {
			var readErr error
			stored, readErr = service.deliveryStore.ReadDeliveredDocument(ctx, record.BindingID, record.Applied.Generation)
			return readErr
		}); err != nil {
			refuse(err)
			continue
		}
		if stored == nil {
			refuse(errors.New("the inbox holds no carrier for the applied generation"))
			continue
		}
		delivered, err := solutionhost.VerifyDelivered(ctx, carrierOf(stored), r.reconciler.verifier)
		if err != nil {
			refuse(err)
			continue
		}
		verified, err := delivered.Manifest()
		if err != nil {
			refuse(err)
			continue
		}
		digest, err := verified.Digest()
		if err != nil || digest != record.Applied.Digest {
			refuse(errors.New("the stored carrier is not the applied manifest"))
			continue
		}
		manifests = append(manifests, manifest)
	}
	return manifests, refusals
}

// oneWorkloadOnePrincipal refuses every principal whose workload another
// principal also runs as. A pod's token names its namespace and ServiceAccount
// — never its container — so two principals sharing one would let one module's
// pod token authorize the other's prefix.
func (p *publishedModuleRegistry) oneWorkloadOnePrincipal() []error {
	holders := map[ModuleWorkload][]string{}
	for id, grant := range p.registry {
		for _, workload := range grant.Workloads {
			key := ModuleWorkload{Namespace: workload.Namespace, ServiceAccount: workload.ServiceAccount}
			if !slices.Contains(holders[key], id) {
				holders[key] = append(holders[key], id)
			}
		}
	}
	var refusals []error
	for key, ids := range holders {
		if len(ids) < 2 {
			continue
		}
		var prefixes []string
		for _, id := range ids {
			if grant, held := p.registry[id]; held {
				prefixes = append(prefixes, grant.Prefix)
			}
			delete(p.registry, id)
			delete(p.sources, id)
		}
		sort.Strings(prefixes)
		refusals = append(refusals, fmt.Errorf("%w: %v all run as workload %s/%s, and one workload maps to exactly one principal",
			ErrModulePrincipalOmitted, prefixes, key.Namespace, key.ServiceAccount))
	}
	return refusals
}

// deriveModulePrincipalGrant is the envelope's grant intersected with what the
// manifest asks for. Every list keeps only what both name; an operation binding
// is granted only when the manifest asks for it on the envelope's audience, and
// then only the scopes both name, per operation.
func deriveModulePrincipalGrant(
	prefix string, entry ModuleEnvelopePrincipal, manifest *solutionhost.ModuleManifest, workloads []ModuleWorkload,
) (ModulePrincipalGrant, []error, error) {
	ceiling := entry.Grant
	request := manifest.Authority
	grant := ModulePrincipalGrant{
		PresenceBinding:  manifest.Binding,
		ArtifactPolicies: maps.Clone(ceiling.ArtifactPolicies),
		ReadAudiences:    maps.Clone(ceiling.ReadAudiences),
		Prefix:           prefix,
		Queues:           keepDeclared(ceiling.Queues, request.Queues),
		Namespaces:       keepDeclared(ceiling.Namespaces, request.Namespaces),
		Resources:        keepDeclared(ceiling.Resources, request.ResourceKinds),
		CrossTenant:      ceiling.CrossTenant,
		Tenant:           ceiling.Tenant,
		Workloads:        workloads,
	}
	grant.ExternalNamespaces = keepDeclared(ceiling.ExternalNamespaces, grant.Namespaces)

	var omitted []error
	omit := func(id string, err error) {
		omitted = append(omitted, fmt.Errorf("%w: %q binding %q (manifest %q generation %d): %w",
			ErrModulePrincipalBindingOmitted, prefix, id, manifest.Binding, manifest.Generation, err))
	}
	for _, asked := range request.Bindings {
		allowed, inside := ceiling.OperationAudiences[asked.ID]
		switch {
		case !inside:
			omit(asked.ID, errors.New("the envelope allows no such operation binding"))
			continue
		case allowed.Audience != asked.Audience:
			omit(asked.ID, fmt.Errorf("the manifest asks for audience %q and the envelope allows %q", asked.Audience, allowed.Audience))
			continue
		}
		invoke := scopeSet(asked.Operations[modulecontract.OperationInvoke])
		if len(invoke) == 0 {
			omit(asked.ID, errors.New("the manifest asks for no invoke scope, and a binding is granted through its invoke operation"))
			continue
		}
		derived := ModuleOperationAudience{
			Audience:     allowed.Audience,
			InvokeScopes: intersectUnitScopes(allowed.InvokeScopes, invoke),
		}
		if lookup := scopeSet(asked.Operations[modulecontract.OperationLookup]); len(lookup) > 0 {
			derived.LookupScopes = intersectUnitScopes(allowed.LookupScopes, lookup)
		}
		if headless := scopeSet(asked.Operations[modulecontract.OperationHeadless]); len(headless) > 0 && allowed.HeadlessScopes != nil {
			derived.HeadlessScopes = nilIfEmpty(intersectUnitScopes(allowed.HeadlessScopes, headless))
		}
		if allowed.SourceDelegationScopes != nil {
			derived.SourceDelegationScopes = nilIfEmpty(intersectUnitScopes(allowed.SourceDelegationScopes, invoke))
		}
		if err := validateOperationAudiences(prefix, map[string]ModuleOperationAudience{asked.ID: derived}); err != nil {
			omit(asked.ID, fmt.Errorf("what the manifest asks for and what the envelope allows leave no valid audience: %w", err))
			continue
		}
		if grant.OperationAudiences == nil {
			grant.OperationAudiences = map[string]ModuleOperationAudience{}
		}
		grant.OperationAudiences[asked.ID] = derived
	}
	if err := ValidateModulePrincipalGrant(prefix, grant); err != nil {
		return ModulePrincipalGrant{}, omitted, err
	}
	return grant, omitted, nil
}

// keepDeclared is allowed narrowed to what declared also names, in allowed's
// order.
func keepDeclared(allowed, declared []string) []string {
	var kept []string
	for _, value := range allowed {
		if slices.Contains(declared, value) {
			kept = append(kept, value)
		}
	}
	return kept
}

// scopeSet is a manifest operation's "<resource kind>:<action>" scopes as a set.
func scopeSet(scopes []string) map[string]bool {
	set := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		set[scope] = true
	}
	return set
}

// intersectUnitScopes keeps, per allowed resource kind, the actions the
// manifest also asks for; a kind left with none is dropped.
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
