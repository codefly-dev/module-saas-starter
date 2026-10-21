package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The live values a Work Context is sealed to, answered for one organization.
//
// core/workcontext seals every capability — a person's as much as a module's —
// to an installation and its revision, the owner's epoch, the execution of the
// principal exercising it, and the issuer's authorization revision; and every
// verifier compares each of them against the live value. These are the host's
// answers. They are LIVE READS on every call, minting and verifying alike, and
// one function answers each question for both: a mint that read one source and
// a verifier that read another would agree only by coincidence.
//
// Two kinds of principal own a capability here, and each answer dispatches on
// which one it is asked about:
//
//   - a DECLARED MODULE PRINCIPAL, which the deployment declares rather than
//     registers per organization. Its installation, epoch and revision are the
//     module authority's (LiveModuleAuthority, ModuleOperationContextRevision);
//     its execution is the approved build, and it always bears one.
//   - a person or a registered agent principal, which the organization's rows
//     answer. A person runs no approved build, and neither does a registered
//     agent — this host approves builds for module principals only — so both
//     bear no execution.

// ErrWorkContextSealUnheld reports that a principal does not hold the
// installation a capability names: the installation is not active in the
// organization, or the principal is not an active member of it.
var ErrWorkContextSealUnheld = errors.New("the principal holds no such active installation")

// ErrWorkContextPrincipalUnknown reports a principal this organization has no
// authorization record for. It is never "bears no execution": answering that for
// an identity the host has never heard of would mint a credential nothing can
// revoke by replacing a build.
var ErrWorkContextPrincipalUnknown = errors.New("the principal has no authorization record in this organization")

// ErrWorkContextNoAudienceInstallation reports an audience that names no
// installation active in the organization. A capability is sealed to one, so a
// mint for an audience the organization never installed has nothing to be
// bound to and is refused.
var ErrWorkContextNoAudienceInstallation = errors.New("the audience names no installation active in this organization")

// WorkContextSealStore is the row-backed half: a person's or a registered
// principal's seal, and the organization's installation of a solution.
type WorkContextSealStore interface {
	// WorkContextHeldInstallation answers the live revision of installationID
	// when it is active in orgID and principalID is an active member there, and
	// ErrWorkContextSealUnheld otherwise.
	WorkContextHeldInstallation(ctx context.Context, orgID, principalID, installationID string) (uint64, error)
	// WorkContextPrincipalEpoch answers principalID's authorization revision in
	// orgID, which every change to its authority advances, and
	// ErrWorkContextPrincipalUnknown when there is none.
	WorkContextPrincipalEpoch(ctx context.Context, orgID, principalID string) (uint64, error)
	// WorkContextOwnerRevision answers the authorization revision a capability
	// owned by ownerID in orgID carries: the greater of the organization's and
	// the owner's, for an active member.
	WorkContextOwnerRevision(ctx context.Context, orgID, ownerID string) (uint64, error)
	// WorkContextSolutionInstallation answers the active installation of the
	// solution binding bindingID in orgID, and
	// ErrWorkContextNoAudienceInstallation when there is none.
	WorkContextSolutionInstallation(ctx context.Context, orgID, bindingID string) (string, error)
}

// SetWorkContextSealStore wires the row-backed seal reads.
func (s *Service) SetWorkContextSealStore(store WorkContextSealStore) {
	s.workContextSeals = store
}

func (s *Service) sealStore() (WorkContextSealStore, error) {
	if s.workContextSeals == nil {
		return nil, errors.New("no Work Context seal store is wired, so no seal can be answered")
	}
	return s.workContextSeals, nil
}

// isDeclaredModulePrincipal reports whether the current registry declares
// principalID.
func (s *Service) isDeclaredModulePrincipal(principalID string) bool {
	snapshot := s.moduleRegistrySnapshot()
	if snapshot == nil {
		return false
	}
	_, declared := snapshot.registry[principalID]
	return declared
}

func (s *Service) liveModuleAuthority(ctx context.Context, principalID, orgID, installationID string) (*LiveModuleAuthority, error) {
	if s.moduleAuthority == nil {
		return nil, errors.New("no live module authority read is wired")
	}
	return s.moduleAuthority.LiveModuleAuthority(ctx, principalID, orgID, installationID)
}

// WorkContextSeal answers the live installation a capability owned by
// principalID is sealed to.
func (s *Service) WorkContextSeal(ctx context.Context, orgID, principalID, installationID string) (string, uint64, error) {
	if s.isDeclaredModulePrincipal(principalID) {
		live, err := s.liveModuleAuthority(ctx, principalID, orgID, installationID)
		if err != nil {
			return "", 0, err
		}
		return live.InstallationID, live.InstallationRevision, nil
	}
	store, err := s.sealStore()
	if err != nil {
		return "", 0, err
	}
	revision, err := store.WorkContextHeldInstallation(ctx, orgID, principalID, installationID)
	if err != nil {
		return "", 0, err
	}
	return installationID, revision, nil
}

// WorkContextPrincipalEpoch answers principalID's live epoch.
//
// For a module principal it is the producer epoch the module authority already
// reads and every deferred producer records, so the capability and the work it
// enqueues are revoked by the same lever. Zero names nothing and is refused here
// rather than handed to core as a value.
func (s *Service) WorkContextPrincipalEpoch(ctx context.Context, orgID, principalID string) (uint64, error) {
	if s.isDeclaredModulePrincipal(principalID) {
		live, err := s.liveModuleAuthority(ctx, principalID, orgID, "")
		if err != nil {
			return 0, err
		}
		if live.ProducerEpoch == 0 {
			return 0, fmt.Errorf("%w: module principal %s has no producer epoch in this organization", ErrWorkContextPrincipalUnknown, principalID)
		}
		return live.ProducerEpoch, nil
	}
	store, err := s.sealStore()
	if err != nil {
		return 0, err
	}
	return store.WorkContextPrincipalEpoch(ctx, orgID, principalID)
}

// WorkContextRevision answers the authorization revision a capability owned by
// ownerID carries.
//
// A person's is the greater of the organization's revision and their own, which
// every membership and role change advances. A module principal has no
// row-backed counter; its revision is ModuleOperationContextRevision of its
// grant as installed in this organization, so narrowing that grant or the
// installation's scope revokes every capability it owns.
func (s *Service) WorkContextRevision(ctx context.Context, orgID, ownerID string) (uint64, error) {
	if s.isDeclaredModulePrincipal(ownerID) {
		// A decision about current module authority, like every other, so it
		// is judged from one snapshot fenced against the committed envelope.
		ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
		if fenceErr != nil {
			return 0, fenceErr
		}
		defer releaseAuthority()
		installed, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, s.moduleRegistrySnapshot(),
			ModuleCaller{PrincipalID: ownerID, BoundOrg: orgID})
		if err != nil {
			return 0, err
		}
		return ModuleOperationContextRevision(installed.Grant), nil
	}
	store, err := s.sealStore()
	if err != nil {
		return 0, err
	}
	return store.WorkContextOwnerRevision(ctx, orgID, ownerID)
}

// WorkContextAudienceInstallation answers the installation a capability for
// audience in orgID is sealed to: the organization's installation of the module
// or solution the audience names. The audience must already be in the host's
// vocabulary; this resolves where it is installed, and an organization that
// never installed it gets ErrWorkContextNoAudienceInstallation.
func (s *Service) WorkContextAudienceInstallation(ctx context.Context, orgID, audience string) (string, error) {
	if binding, isSolution := strings.CutPrefix(audience, SolutionAudiencePrefix); isSolution {
		store, err := s.sealStore()
		if err != nil {
			return "", err
		}
		return store.WorkContextSolutionInstallation(ctx, orgID, binding)
	}
	live, err := s.liveModuleAuthority(ctx, ModulePrincipalID(audience), orgID, "")
	if errors.Is(err, ErrModuleInstallationInactive) {
		return "", fmt.Errorf("%w: %s", ErrWorkContextNoAudienceInstallation, audience)
	}
	if err != nil {
		return "", err
	}
	return live.InstallationID, nil
}

// The unit of authority a capability minted from a source delegation exercises,
// sealed into it as core's operation binding.
//
// A delegation is a person's grant to one binding of one module (the delegating
// module), and that binding names the module it may call (its audience). Exactly
// those two modules may USE it: the delegating module, minting from it directly;
// and the audience module, presenting the delegation (by reference, or as the
// parent capability it was handed). The binding id names the delegation AND the
// module using it, and the live read answers that module as the principal the
// binding is granted to only when the issuer's records entitle it — the id is a
// pointer, never authority: guessing one resolves to a refusal.
//
// The capability itself carries the owner's ordinary authorization revision; the
// delegation's liveness is the binding's, read live on every verification.

// sourceDelegationBindingPrefix marks a binding id as a source delegation's.
const sourceDelegationBindingPrefix = "source-delegation/"

// ErrNoOperationBinding reports a binding id that names nothing this
// organization holds — no such delegation, or a module it does not entitle.
var ErrNoOperationBinding = errors.New("no such operation binding")

// SourceDelegationBindingID is the operation binding of delegationID used by
// the module whose prefix is usedBy.
func SourceDelegationBindingID(delegationID, usedBy string) string {
	return sourceDelegationBindingPrefix + delegationID + "/" + usedBy
}

// namesASourceDelegation reports whether a sealed operation binding id names a
// source delegation AT ALL. It is the shape test the source-delegation checks
// need before they claim a context: a person-owned context actored by a module
// is not a source delegation's merely because of its subjects — a module
// executing its own installed operation on a person's behalf has exactly that
// shape and seals its OWN binding. Claiming those and refusing them left the
// runtime's every call to an owner denied as a stale delegation, which it was
// not. The binding id is still only a pointer: it selects the checker here,
// and every fact behind it is re-read by whichever checker runs. Its ABSENCE
// selects nothing and is refused by the delegation checks, because the binding
// travels in the request and a consumer that omitted it would otherwise have a
// narrowed delegation confirmed against a broader installation.
func namesASourceDelegation(id string) bool {
	_, _, ok := parseSourceDelegationBindingID(id)
	return ok
}

func parseSourceDelegationBindingID(id string) (delegationID, usedBy string, ok bool) {
	rest, isDelegation := strings.CutPrefix(id, sourceDelegationBindingPrefix)
	if !isDelegation {
		return "", "", false
	}
	delegationID, usedBy, found := strings.Cut(rest, "/")
	if !found || delegationID == "" || usedBy == "" || strings.Contains(usedBy, "/") {
		return "", "", false
	}
	return delegationID, usedBy, true
}

// WorkContextOperationBinding is the live state of one operation binding.
type WorkContextOperationBinding struct {
	ID             string
	PrincipalID    string
	InstallationID string
	Revision       uint64
	Incarnation    uint64
	Revoked        bool
}

// WorkContextOperationBinding answers one binding by its id, exactly.
//
// Revoked follows the delegation, whose revocation is terminal. The revision is
// the using module's installation revision, so a capability sealed before that
// installation's scope or status moved stops verifying. A binding the records no
// longer support — the delegating module's binding changed, the source is gone,
// the person lost the role that let them delegate — is refused outright rather
// than answered, exactly as a mint from it is.
func (s *Service) WorkContextOperationBinding(ctx context.Context, orgID, bindingID string) (WorkContextOperationBinding, error) {
	if NamesInstalledBinding(bindingID) {
		return s.resolveInstalledBinding(ctx, orgID, bindingID)
	}
	delegationID, usedBy, ok := parseSourceDelegationBindingID(bindingID)
	if !ok {
		return WorkContextOperationBinding{}, ErrNoOperationBinding
	}
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return WorkContextOperationBinding{}, fenceErr
	}
	defer releaseAuthority()

	snapshot := s.moduleRegistrySnapshot()
	user := ModulePrincipalID(usedBy)
	var binding WorkContextOperationBinding
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		delegation, err := s.store.GetSourceDelegation(ctx, orgID, delegationID)
		if err != nil {
			return err
		}
		if delegation == nil || delegation.OrgID != orgID {
			return ErrNoOperationBinding
		}
		delegator, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot,
			ModuleCaller{PrincipalID: ModulePrincipalID(delegation.ModulePrefix), BoundOrg: orgID})
		if err != nil {
			return err
		}
		granted, state := delegator.Grant.currentSourceDelegationBinding(delegation)
		switch state {
		case sourceDelegationBindingChanged:
			return ErrSourceDelegationContextStale
		case sourceDelegationBindingUnavailable:
			return ErrSourceDelegationBindingUnavailable
		}
		if usedBy != delegation.ModulePrefix && usedBy != granted.Audience {
			return ErrNoOperationBinding
		}
		using, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: user, BoundOrg: orgID})
		if err != nil {
			return err
		}
		binding = WorkContextOperationBinding{
			ID: bindingID, PrincipalID: user, InstallationID: using.InstallationID,
			Revision: using.InstallationRevision, Incarnation: 1, Revoked: !delegation.Active(),
		}
		if binding.Revoked {
			return nil
		}
		facts, err := s.store.SourceDelegationFacts(ctx, orgID, delegation.PrincipalID, delegation.SourceID)
		if err != nil {
			return err
		}
		if facts.revocation() != "" {
			return ErrSourceDelegationContextStale
		}
		return nil
	})
	if err != nil {
		return WorkContextOperationBinding{}, err
	}
	return binding, nil
}
