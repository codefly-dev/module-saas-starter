package business

// The authority core decides what an installation's holders hold from the
// declaration its consent approved, kept per module and generation
// (authority_core_approved_modules, migration 68). A consent the core takes or
// advances writes its own. A consent recorded before the core kept them names
// a generation the store may hold nothing for: migration 68 writes the
// declaration only where that generation is still the delivered one, because
// the module's current declaration is the approved one only then. Every older
// generation is rebuilt here, at boot, from the signed documents this host
// received for it, which the delivery inbox keeps append-only.

import (
	"context"
	"errors"
	"fmt"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/decide"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	corescope "github.com/codefly-dev/authority/libraries/authority-core/go/scope"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/core/wool"

	compositionv1 "accounts/pkg/gen/host/composition/v1"
)

// approvedGeneration is one (target, generation) a live consent names and the
// store keeps no declaration for, with the installations that name it and the
// target as the core composes it now.
type approvedGeneration struct {
	target     string
	generation uint64
	current    domain.Module
	held       []domain.Installation
}

// keepApprovedDeclarations writes, for every pending or active installation
// whose consent names a generation the store keeps no declaration for, the
// declaration of that generation: the composed module when the generation is
// still the delivered one, otherwise the module rebuilt from the documents the
// delivery inbox holds for the target's binding at that generation, exactly as
// a delivery composes it. Where the inbox holds no such document (or one that
// does not compose to the target as the core holds it), nothing is written:
// the core then refuses issuance through that installation by name, the
// installation reads as upgrade_available, and one line is logged naming it
// and the generation. A declaration the store already keeps is never
// replaced, so a second run writes nothing.
func (s *Service) keepApprovedDeclarations(ctx context.Context) error {
	cores, ok := s.store.(AuthorityCoreStore)
	if !ok {
		return nil
	}
	store := cores.AuthorityCoreWithConsent(nil)
	missing, err := unkeptApprovedGenerations(ctx, store)
	if err != nil || len(missing) == 0 {
		return err
	}
	bindings, err := s.ListSolutionHostBindings(ctx)
	if err != nil {
		return err
	}
	log := wool.Get(ctx).In("keepApprovedDeclarations")
	var failures []error
	for _, want := range missing {
		approved, reason, err := s.approvedDeclaration(ctx, bindings, want)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s generation %d: %w", want.target, want.generation, err))
			continue
		}
		if reason != "" {
			for _, inst := range want.held {
				log.Warn("the declaration this installation's consent approved is not kept and cannot be rebuilt; the authority core refuses issuance through it until an administrator reviews and installs the delivered release",
					wool.Field("installation", inst.ID), wool.Field("organization", inst.Organization), wool.Field("target", want.target),
					wool.Field("generation", want.generation), wool.Field("reason", reason))
			}
			continue
		}
		if err := store.Write(ctx, func(tx authority.Tx) error {
			if _, err := tx.ApprovedModule(want.target, want.generation); !errors.Is(err, domain.ErrNotFound) {
				return err // kept already (nil), or unreadable
			}
			return tx.PutApprovedModule(approved)
		}); err != nil {
			failures = append(failures, fmt.Errorf("%s generation %d: %w", want.target, want.generation, err))
		}
	}
	return errors.Join(failures...)
}

// unkeptApprovedGenerations lists every (target, generation) a pending or
// active installation's consent names, at or below the target's composed
// generation, that the store keeps no declaration for.
func unkeptApprovedGenerations(ctx context.Context, store authority.Store) ([]*approvedGeneration, error) {
	var out []*approvedGeneration
	err := store.Read(ctx, func(tx authority.Tx) error {
		out = nil
		all, err := tx.Installations("")
		if err != nil {
			return err
		}
		byKey := map[string]*approvedGeneration{}
		for _, inst := range all {
			c := inst.Consent
			if (inst.Status != domain.Pending && inst.Status != domain.Active) || c.OperationID == "" || c.Generation == 0 {
				continue
			}
			m, err := tx.ModuleNamed(inst.Target)
			if errors.Is(err, domain.ErrNotFound) {
				continue // withdrawn: nothing issues through it
			}
			if err != nil {
				return err
			}
			if c.Generation > m.Generation {
				continue
			}
			key := fmt.Sprintf("%s@%d", inst.Target, c.Generation)
			if want, seen := byKey[key]; seen {
				want.held = append(want.held, inst)
				continue
			}
			if _, err := tx.ApprovedModule(inst.Target, c.Generation); !errors.Is(err, domain.ErrNotFound) {
				if err != nil {
					return err
				}
				continue
			}
			want := &approvedGeneration{target: inst.Target, generation: c.Generation, current: m, held: []domain.Installation{inst}}
			byKey[key] = want
			out = append(out, want)
		}
		return nil
	})
	return out, err
}

// approvedDeclaration is the declaration of want's generation, or the reason
// it cannot be had: the composed module when that generation is still
// delivered, otherwise the module the inbox's documents for the target's
// binding at that generation compose to, which must be the target as the core
// holds it (its name, principal and kind) at that generation.
func (s *Service) approvedDeclaration(ctx context.Context, bindings []*SolutionHostBindingRecord, want *approvedGeneration) (domain.Module, string, error) {
	if want.generation == want.current.Generation {
		return want.current, "", nil
	}
	if s.deliveryStore == nil {
		return domain.Module{}, "this host keeps no delivery inbox", nil
	}
	binding := ""
	for _, record := range bindings {
		if record.Applied == nil || record.Applied.SolutionID != want.target {
			continue
		}
		if binding != "" {
			return domain.Module{}, fmt.Sprintf("bindings %s and %s both apply %s", binding, record.BindingID, want.target), nil
		}
		binding = record.BindingID
	}
	if binding == "" {
		return domain.Module{}, "no binding applies the target", nil
	}
	var delivered *SolutionDeliveryRecord
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		delivered, err = s.deliveryStore.ReadDeliveredDocument(ctx, binding, want.generation)
		return err
	}); err != nil {
		return domain.Module{}, "", err
	}
	if delivered == nil {
		return domain.Module{}, fmt.Sprintf("the delivery inbox holds no document for binding %s at generation %d", binding, want.generation), nil
	}
	m, err := deliveredCoreModule(delivered)
	if err != nil {
		return domain.Module{}, fmt.Sprintf("binding %s generation %d does not compose: %v", binding, want.generation, err), nil
	}
	if m.Name != want.target || m.Generation != want.generation || m.Principal != want.current.Principal || m.Kind != want.current.Kind {
		return domain.Module{}, fmt.Sprintf("binding %s generation %d composes %s %s@%d (principal %q), not the target", binding, want.generation, m.Kind, m.Name, m.Generation, m.Principal), nil
	}
	return m, "", nil
}

// deliveredCoreModule is the module the core composes from one received
// generation of a binding: its verified carrier and the authority document
// paired with it, read as the delivery reads them (coreModuleOf), and held
// to what the core keeps of a module it applies.
func deliveredCoreModule(delivered *SolutionDeliveryRecord) (domain.Module, error) {
	manifest, err := solutionhost.ManifestFromVerified(delivered.Payload)
	if err != nil {
		return domain.Module{}, err
	}
	if manifest.Removed {
		return domain.Module{}, errors.New("the generation is a withdrawal")
	}
	m, err := coreModuleOf(manifest, delivered.Payload, signedAuthorityOf(delivered))
	if err != nil {
		return domain.Module{}, err
	}
	if err := decide.ValidateModule(m); err != nil {
		return domain.Module{}, err
	}
	m.ResourceKinds = corescope.Normalize(decide.Served(m))
	return m, nil
}

// signedAuthorityOf is the authority document received with delivered, or nil.
func signedAuthorityOf(delivered *SolutionDeliveryRecord) *compositionv1.SignedManifestV1 {
	if len(delivered.AuthorityDocument) == 0 {
		return nil
	}
	return &compositionv1.SignedManifestV1{Document: delivered.AuthorityDocument, Envelope: delivered.AuthorityEnvelope}
}

// ApprovedDeclaration is the declaration the core decides inst's holders
// from, given m, its target as composed now: m while inst's consent stands at
// m's generation or names none (no consent, or one recorded before the
// generation was kept), otherwise the declaration that consent approved, as
// the store keeps it. It is the core's own rule (approvedModule in the
// authority core), read for a host that lists what a holder may hold. An
// approved generation the store does not keep answers domain.ErrNotFound: the
// core refuses that installation's holders.
func (s *Service) ApprovedDeclaration(ctx context.Context, inst domain.Installation, m domain.Module) (domain.Module, error) {
	c := inst.Consent
	if c.OperationID == "" || c.Generation == 0 || c.Generation >= m.Generation || inst.Target != m.Name {
		return m, nil
	}
	cores, ok := s.store.(AuthorityCoreStore)
	if !ok {
		return domain.Module{}, ErrAuthorityCoreUnconfigured
	}
	var approved domain.Module
	err := cores.AuthorityCoreWithConsent(nil).Read(ctx, func(tx authority.Tx) error {
		var err error
		approved, err = tx.ApprovedModule(m.Name, c.Generation)
		return err
	})
	return approved, err
}
