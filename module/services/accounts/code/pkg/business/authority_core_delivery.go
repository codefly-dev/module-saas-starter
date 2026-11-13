package business

// A delivery composes a module twice over: accounts' own delivery records it
// (the signed carrier, its applied generation, the registry every route and
// workload review reads), and the authority core records the module it
// decides with. Both read the same two signed documents; the core reads them
// through its shared delivered package, never a reading of its own here.

import (
	"context"
	"errors"
	"fmt"

	coredelivered "github.com/codefly-dev/authority/libraries/authority-core/go/delivered"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"
	"google.golang.org/protobuf/proto"

	authorityv1 "accounts/pkg/gen/host/authority/v1"
	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

// coreModuleOf is what the core composes from a delivery's manifest and the
// authority document paired with it, if any, named by the source it was read
// from (coredelivered.Source): an empty authority document is none, as the
// inbox keeps it.
func coreModuleOf(manifest *solutionhost.ModuleManifest, carrierDocument []byte, signed *compositionv1.SignedManifestV1) (domain.Module, error) {
	m, err := readCoreModule(manifest, carrierDocument, signed)
	var document []byte
	if len(signed.GetDocument()) > 0 {
		document = signed.GetDocument()
	}
	m.Source = coredelivered.Source(carrierDocument, document)
	return m, err
}

func readCoreModule(manifest *solutionhost.ModuleManifest, carrierDocument []byte, signed *compositionv1.SignedManifestV1) (domain.Module, error) {
	var doc *coredelivered.Document
	if signed != nil {
		var wire compositionv1.AuthorityManifestV1
		if err := proto.Unmarshal(signed.GetDocument(), &wire); err != nil {
			return domain.Module{}, domain.Malformed("the authority's document does not read: %v", err)
		}
		doc = documentOf(&wire)
		if err := coredelivered.Pair(*doc, carrierDocument, manifest); err != nil {
			return domain.Module{}, err
		}
	}
	return coredelivered.Module(manifest, doc)
}

// documentOf reads this host's binding of the authority's document as the core's.
func documentOf(in *compositionv1.AuthorityManifestV1) *coredelivered.Document {
	doc := &coredelivered.Document{Module: in.GetModule(), Generation: in.GetGeneration(), CoreManifestDigest: in.GetCoreManifestDigest(),
		ScopeCeilings: wireScopes(in.GetScopeCeilings()), OwnerAware: in.GetOwnerAware(), InstallationRole: in.GetInstallationRole()}
	for _, edge := range in.GetEdgeDependencies() {
		doc.EdgeDependencies = append(doc.EdgeDependencies, domain.EdgeDependency{Module: edge.GetModule(), Presence: edge.GetPresence()})
	}
	doc.SharedKinds = append(doc.SharedKinds, in.GetSharedKinds()...)
	for _, m := range in.GetMethods() {
		doc.Methods = append(doc.Methods, coredelivered.Method{Method: m.GetMethod(), Requires: wireScopes(m.GetRequires()), AnyCheckedCaller: m.GetAnyCheckedCaller()})
	}
	for _, r := range in.GetRoles() {
		role := coredelivered.Role{Name: r.GetName(), Scopes: wireScopes(r.GetScopes()), InstallationOwned: wireScopes(r.GetInstallationOwnedScopes())}
		for _, through := range r.GetThrough() {
			if role.Through == nil {
				role.Through = map[string]coredelivered.Scopes{}
			}
			role.Through[through.GetBinding()] = append(role.Through[through.GetBinding()], wireScopes(through.GetScopes())...)
		}
		doc.Roles = append(doc.Roles, role)
	}
	return doc
}

// wireScopes converts the interface's scopes to core's, byte for byte.
func wireScopes(in []*authorityv1.WorkScopeV1) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(in))
	for _, sc := range in {
		raw, _ := proto.Marshal(sc)
		c := &basev0.WorkScopeV1{}
		_ = proto.Unmarshal(raw, c)
		out = append(out, c)
	}
	return out
}

// precheckCoreModule refuses, before anything is recorded, a delivery the core
// would refuse to compose: the core's own admission (authority.Service.Admit),
// read-only against the composition as it stands, so the delivery endpoint
// answers the core's reason at once. It reads the carrier unverified, so it
// only ever refuses: what is applied is read again from the verified record,
// and the pass composes it in the core before accounts records it
// (composeDelivered), which is what keeps the two records together. A host
// whose core is not built yet admits nothing: the delivery is answered with
// that, and comes again once the core is built. A
// module that gains or loses roles keeps its installations as they are: every
// module is installed by composition, and its roles decide only who may use
// it.
func (s *Service) precheckCoreModule(ctx context.Context, req *compositionv1.ApplyManifestRequest) error {
	manifest, err := solutionhost.ManifestFromVerified(req.GetCarrier().GetDocument())
	if err != nil {
		return nil // the delivery path names a carrier that does not read
	}
	if manifest.Removed {
		return nil
	}
	m, err := coreModuleOf(manifest, req.GetCarrier().GetDocument(), req.GetAuthority())
	if err != nil {
		return err
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return coreUnconfiguredYet(err)
	}
	return core.Admit(ctx, m)
}

// applyCoreModule records a verified delivery in the core: the module it
// composes at its generation, or its withdrawal. A replay of the generation
// the core already holds changes nothing; what the core refuses is refused
// with its reason, and a pass records nothing for it (composeDelivered). A
// host whose core is not built yet composes nothing, and says so without a
// verdict: the pass records the generation as pending and asks again at its
// next pass, which is what keeps a restart from applying a generation the
// core has not admitted (the reconciler's first pass runs before the core is
// built, and a generation the core refused yesterday is admissible to
// accounts' own admission every pass).
func (s *Service) applyCoreModule(ctx context.Context, manifest *solutionhost.ModuleManifest, carrierDocument []byte, signed *compositionv1.SignedManifestV1) ([]string, error) {
	core, err := s.AuthorityCore()
	if err != nil {
		return nil, coreUnconfiguredYet(err)
	}
	if manifest.Removed {
		// A withdrawal is idempotent: two passes may apply the same one at
		// once (the delivery endpoint's and the reconciler's), and a module the
		// core no longer composes is already withdrawn. Reading first and
		// withdrawing after raced exactly that way; the withdrawal's own
		// NotFound is the one answer that is safe to read.
		if err := core.WithdrawModule(ctx, manifest.Alias); err != nil {
			if code, ok := domain.CodeOf(err); ok && code == domain.NotFound {
				return nil, nil
			}
			return nil, err
		}
		return nil, nil
	}
	m, err := coreModuleOf(manifest, carrierDocument, signed)
	if err != nil {
		return nil, err
	}
	// The core decides a replay of the generation it holds on its own
	// transaction (ApplyModule): read here first, a slower path was refused
	// the generation a faster one had just composed.
	return core.ApplyModule(ctx, m)
}

// coreUnconfiguredYet is the answer of a host whose core is not built yet: not
// a verdict on the delivery (it carries no domain code), so the pass keeps it
// pending and the endpoint answers it as this host's state.
func coreUnconfiguredYet(err error) error {
	if errors.Is(err, ErrAuthorityCoreUnconfigured) {
		return fmt.Errorf("%w yet", ErrAuthorityCoreUnconfigured)
	}
	return err
}

// coreDeliveryError is a refusal of the core's, carried through the delivery
// path's own error vocabulary.
func coreDeliveryError(err error) error {
	if err == nil || errors.Is(err, ErrSolutionDeliveryConflict) {
		return err
	}
	code, ok := domain.CodeOf(err)
	switch {
	case !ok:
		return fmt.Errorf("%w: the authority core: %w", ErrSolutionDeliveryHostState, err)
	case code == domain.Invalid:
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryMalformed, err)
	case code == domain.Precondition:
		return fmt.Errorf("%w: %w", ErrSolutionDeliveryConflict, err)
	}
	return fmt.Errorf("%w: %w", ErrSolutionDeliveryUnattested, err)
}

// ReconcileHeldDeliveries composes, in the core, every delivery accounts has
// applied: what a host runs once its core is built (its boot reconciliation),
// so a module delivered before this host decided through the core is composed
// there too (a delivery that arrives while the core is not built waits for it
// instead, applyCoreModule), and a generation the core holds is read again as
// this host reads it now (rereadHeldGeneration).
func (s *Service) ReconcileHeldDeliveries(ctx context.Context) error {
	if s.deliveryStore == nil {
		return nil
	}
	records, err := s.ListSolutionHostBindings(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, record := range records {
		if err := s.composeAppliedDelivery(ctx, record); err != nil {
			failures = append(failures, fmt.Errorf("binding %s: %w", record.BindingID, err))
		}
	}
	return errors.Join(failures...)
}

// composeAppliedDelivery composes one binding's applied generation in the
// core, read from the inbox's exact applied generation: the signed documents
// accounts verified when it took them. A generation the core already holds
// changes nothing.
func (s *Service) composeAppliedDelivery(ctx context.Context, record *SolutionHostBindingRecord) error {
	if s.deliveryStore == nil || record == nil {
		return nil
	}
	manifest, err := record.AppliedManifest()
	if err != nil || manifest == nil {
		return nil
	}
	if handled, err := s.rereadHeldGeneration(ctx, record, manifest); handled || err != nil {
		return err
	}
	delivered, err := s.deliveredGeneration(ctx, record.BindingID, record.Applied.Generation)
	if err != nil || delivered == nil {
		return err
	}
	_, err = s.applyCoreModule(ctx, manifest, delivered.Payload, signedAuthorityOf(delivered))
	return err
}

// rereadHeldGeneration reads the applied generation the core already holds
// again from the inbox's record of it, verified again with the pinned keys,
// as this host reads a delivery now (authority.Service.RereadModule). A host
// that learned to read a declaration field reads the same signed documents
// as another declaration than the one it composed before, which the core
// otherwise refuses as a changed declaration at the held generation, leaving
// the module stranded until its next release and any approval taken
// meanwhile reviewing the copy without the field. The core proves the
// re-read is of the delivery it holds (the source digest of the documents),
// holds every consent at what it approved, and offers the upgrade; a
// re-read that changes the held declaration is logged, once, since the next
// start reads the same declaration again. It handles nothing (false) where
// the core does not hold the applied generation or the host verifies no
// inbox: the generation is applied as a delivery is.
func (s *Service) rereadHeldGeneration(ctx context.Context, record *SolutionHostBindingRecord, manifest *solutionhost.ModuleManifest) (bool, error) {
	if s.deliveryVerifier == nil || manifest.Removed {
		return false, nil
	}
	core, err := s.AuthorityCore()
	if err != nil {
		return false, nil // applied when the core is built (applyCoreModule)
	}
	held, err := core.Composed(ctx, manifest.Alias)
	if err != nil || held.StandIn || held.Generation != manifest.Generation {
		return false, nil
	}
	delivered, verified, _, err := s.verifyDeliveredGeneration(ctx, manifest.Alias, record.BindingID, manifest.Generation)
	if err != nil || delivered == nil {
		return true, err
	}
	again, err := verified.Manifest()
	if err != nil {
		return true, err
	}
	m, err := coreModuleOf(again, delivered.Payload, signedAuthorityOf(delivered))
	if err != nil {
		return true, err
	}
	reread, err := core.RereadModule(ctx, m)
	if reread {
		wool.Get(ctx).In("rereadHeldGeneration").Info("read a held generation again: its declaration as this host reads the delivery differs from the one composed from it before; consented installations stay on what they approved and offer the upgrade",
			wool.Field("module", m.Name), wool.Field("generation", m.Generation), wool.Field("binding", record.BindingID), wool.Field("source", m.Source))
	}
	return true, err
}

// composeDelivered composes a delivered generation in the core before
// accounts applies it: the carrier the pass verified, with the authority
// document the inbox holds for that generation. The core's refusal is the
// pass's verdict on the generation: accounts records it as refused and
// applies nothing, so what accounts holds as applied is what the core
// composes. The core is asked first and accounts records after, rather than
// the other way round, because the core's record is the one that cannot be
// moved by hand: a generation accounts applied that the core refused stayed
// applied and current for a day (Blue, 2026-11-11: a directory page's
// generation 8, refused on reach, recorded as current beside the core's 7;
// its generation 9 then measured as a widening of 7 and ended three of the
// page's grants).
// Should accounts' apply fail after the core composed, the core is one
// generation ahead, which the next pass settles: the core answers the
// generation it holds as a replay and accounts applies it.
// The authority document is read from the inbox's record of the generation,
// never assumed absent: a module composed without its document is held at a
// digest its redelivery with the document then fails as a new generation, so
// a host with no inbox, or an inbox holding no record of the generation,
// composes nothing and says so.
func (s *Service) composeDelivered(ctx context.Context, delivered *solutionhost.Delivered, manifest *solutionhost.ModuleManifest) error {
	if s.deliveryStore == nil {
		return fmt.Errorf("generation %d of binding %s is composed from its delivered record, and this host keeps no inbox", manifest.Generation, manifest.Binding)
	}
	record, err := s.deliveredGeneration(ctx, manifest.Binding, manifest.Generation)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("generation %d of binding %s is composed from its delivered record, which the inbox does not hold", manifest.Generation, manifest.Binding)
	}
	_, err = s.applyCoreModule(ctx, manifest, delivered.Payload(), signedAuthorityOf(record))
	return err
}

// deliveredGeneration is the inbox's record of binding's generation, nil when
// the inbox holds none.
func (s *Service) deliveredGeneration(ctx context.Context, binding string, generation uint64) (*SolutionDeliveryRecord, error) {
	var delivered *SolutionDeliveryRecord
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		delivered, err = s.deliveryStore.ReadDeliveredDocument(ctx, binding, generation)
		return err
	})
	return delivered, err
}
