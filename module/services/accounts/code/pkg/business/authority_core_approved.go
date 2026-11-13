package business

// The authority core decides what an installation's holders hold from the
// declaration its consent approved, kept per module and generation
// (authority_core_approved_modules, migration 68). A consent the core takes or
// advances writes its own. A consent recorded before the core kept them names
// a generation the store may hold nothing for: migration 68 writes the
// declaration only where that generation is still the delivered one, because
// the module's current declaration is the approved one only then. Every older
// generation is rebuilt here, at boot and on every delivery pass, from the
// signed documents this host received for it, which the delivery inbox keeps
// append-only: verified again as delivery verified them, and written only when
// it is the offer the consents naming it approved.

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

// approvedGeneration is one (target, generation, offer digest) a live consent
// names and the store keeps no declaration for, with the installations that
// name it and the target as the core composes it now.
type approvedGeneration struct {
	target     string
	generation uint64
	digest     string
	current    domain.Module
	held       []domain.Installation
}

// errNotTheApprovedOffer rolls back the write of a rebuilt declaration that
// is not the offer every consent naming its generation approved.
var errNotTheApprovedOffer = errors.New("the rebuilt declaration is not the offer a consent approved")

// keepApprovedDeclarations writes, for every pending or active installation
// whose consent names a generation the store keeps no declaration for, the
// declaration of that generation: the composed module when the generation is
// still the delivered one, otherwise the module rebuilt from the documents the
// delivery inbox holds for the target's binding at that generation, verified
// again with the pinned keys exactly as delivery verified them, from the
// binding's own ownership domain. It is written only when its offer digest, as
// the store answers it, is the one every installation naming that generation
// consented to (ConsentReceipt.Digest); otherwise nothing is written for any
// of them. Where nothing is written, the core refuses issuance through those
// installations by name, they read as upgrade_available, and one line is
// logged per installation naming it, the generation and why. A declaration
// the store already keeps is never replaced, so a later run writes only what
// an earlier one could not (a document that has arrived since).
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
	unkept := func(want *approvedGeneration, reason func(domain.Installation) string) {
		for _, inst := range want.held {
			log.Warn("the declaration this installation's consent approved is not kept and is not written; the authority core refuses issuance through it until an administrator reviews and installs the delivered release",
				wool.Field("installation", inst.ID), wool.Field("organization", inst.Organization), wool.Field("target", want.target),
				wool.Field("generation", want.generation), wool.Field("reason", reason(inst)))
		}
	}
	var failures []error
	for _, want := range missing {
		approved, reason, err := s.approvedDeclaration(ctx, bindings, want)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s generation %d: %w", want.target, want.generation, err))
			continue
		}
		if reason != "" {
			unkept(want, func(domain.Installation) string { return reason })
			continue
		}
		// The digest is the one a consent records: of the declaration as the
		// store answers it, which keys what it keeps, so the write is read
		// back by the consents' digest in the same transaction, and a
		// declaration that is not that offer is rolled back.
		err = store.Write(ctx, func(tx authority.Tx) error {
			if _, err := tx.ApprovedModule(want.target, want.generation, want.digest); !errors.Is(err, domain.ErrNotFound) {
				return err // kept already (nil), or unreadable
			}
			if err := tx.PutApprovedModule(approved); err != nil {
				return err
			}
			if _, err := tx.ApprovedModule(want.target, want.generation, want.digest); errors.Is(err, domain.ErrNotFound) {
				return errNotTheApprovedOffer
			} else if err != nil {
				return err
			}
			return nil
		})
		switch {
		case errors.Is(err, errNotTheApprovedOffer):
			composed, _ := authority.OfferDigest(approved)
			unkept(want, func(inst domain.Installation) string {
				return fmt.Sprintf("its consent approved offer %q, and the inbox's documents for generation %d compose %q", inst.Consent.Digest, want.generation, composed)
			})
		case err != nil:
			failures = append(failures, fmt.Errorf("%s generation %d: %w", want.target, want.generation, err))
		}
	}
	return errors.Join(failures...)
}

// unkeptApprovedGenerations lists every (target, generation, offer digest) a
// pending or active installation's consent names, at or below the target's
// composed generation, that the store keeps no declaration for.
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
			key := fmt.Sprintf("%s@%d@%s", inst.Target, c.Generation, c.Digest)
			if want, seen := byKey[key]; seen {
				want.held = append(want.held, inst)
				continue
			}
			if _, err := tx.ApprovedModule(inst.Target, c.Generation, c.Digest); !errors.Is(err, domain.ErrNotFound) {
				if err != nil {
					return err
				}
				continue
			}
			want := &approvedGeneration{target: inst.Target, generation: c.Generation, digest: c.Digest, current: m, held: []domain.Installation{inst}}
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
// binding at that generation compose to. Those documents are verified again
// with the pinned keys (verifyDeliveredGeneration, as delivery verifies
// them), must speak for the ownership domain the binding is applied under,
// and must compose the target as the core holds it (its name, principal and
// kind) at that generation.
func (s *Service) approvedDeclaration(ctx context.Context, bindings []*SolutionHostBindingRecord, want *approvedGeneration) (domain.Module, string, error) {
	if want.generation == want.current.Generation {
		return want.current, "", nil
	}
	if s.deliveryStore == nil || s.deliveryVerifier == nil {
		return domain.Module{}, "this host verifies no delivery inbox", nil
	}
	var binding *SolutionHostBindingRecord
	for _, record := range bindings {
		if record.Applied == nil || record.Applied.SolutionID != want.target {
			continue
		}
		if binding != nil {
			return domain.Module{}, fmt.Sprintf("bindings %s and %s both apply %s", binding.BindingID, record.BindingID, want.target), nil
		}
		binding = record
	}
	if binding == nil {
		return domain.Module{}, "no binding applies the target", nil
	}
	delivered, verified, _, err := s.verifyDeliveredGeneration(ctx, want.target, binding.BindingID, want.generation)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.Module{}, "", err
		}
		return domain.Module{}, fmt.Sprintf("binding %s generation %d does not verify: %v", binding.BindingID, want.generation, err), nil
	}
	if delivered == nil {
		return domain.Module{}, fmt.Sprintf("the delivery inbox holds no document for binding %s at generation %d", binding.BindingID, want.generation), nil
	}
	applied := binding.ownershipDomain()
	manifest, err := verified.Manifest()
	if err != nil {
		return domain.Module{}, fmt.Sprintf("binding %s generation %d does not read: %v", binding.BindingID, want.generation, err), nil
	}
	if applied == "" || delivered.OwnershipDomain != applied || manifest.Host.Domain != applied {
		return domain.Module{}, fmt.Sprintf("binding %s is applied under ownership domain %q, and its generation %d speaks for %q", binding.BindingID, applied, want.generation, manifest.Host.Domain), nil
	}
	m, err := deliveredCoreModule(delivered)
	if err != nil {
		return domain.Module{}, fmt.Sprintf("binding %s generation %d does not compose: %v", binding.BindingID, want.generation, err), nil
	}
	if m.Name != want.target || m.Generation != want.generation || m.Principal != want.current.Principal || m.Kind != want.current.Kind {
		return domain.Module{}, fmt.Sprintf("binding %s generation %d composes %s %s@%d (principal %q), not the target", binding.BindingID, want.generation, m.Kind, m.Name, m.Generation, m.Principal), nil
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
