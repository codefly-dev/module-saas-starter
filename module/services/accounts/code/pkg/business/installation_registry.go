package business

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InstallationRegistryEntry joins an applied target to the ONE principal that
// delivered authority associates with its binding. Ambiguity is a refusal; an
// alias, release name or installation agent is never guessed to be a principal.
func installationRegistryEntry(snapshot *installedModuleRegistry, bindingID, orgID string) (string, ModulePrincipalGrant, error) {
	var id string
	var grant ModulePrincipalGrant
	for principal, candidate := range snapshot.registry {
		if candidate.PresenceBinding != bindingID || bindingID == "" {
			continue
		}
		if id != "" {
			return "", ModulePrincipalGrant{}, status.Error(codes.FailedPrecondition, "registry binding has ambiguous module principals")
		}
		id, grant = principal, candidate
	}
	if id == "" || !moduleAvailableToOrganization(grant, orgID) {
		return "", ModulePrincipalGrant{}, status.Error(codes.PermissionDenied, "registry entry is not available to this organization")
	}
	return id, grant, nil
}

func (s *Service) prepareInstallation(ctx context.Context, params *InstallSolutionParams) (*installedModuleRegistry, error) {
	snapshot := s.moduleRegistrySnapshot()
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	var available []*AvailableSolutionTarget
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		available, err = s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{OrgID: params.OrgID, TargetID: params.TargetID, Limit: 1})
		return err
	}); err != nil {
		return nil, err
	}
	if len(available) != 1 {
		return nil, NewStoreError(ErrSolutionTargetNotInstallable, ErrTypeValidation)
	}
	target := available[0]
	if params.Approval != nil {
		if err := params.Approval.Validate(); err != nil {
			return nil, err
		}
		if params.Approval.TargetID != target.TargetID || params.Approval.Generation != target.AppliedGeneration {
			return nil, InstallationApprovalRefusal("INSTALLATION_APPROVAL_GENERATION_CHANGED", "the reviewed target or generation is no longer current; review ListAvailableSolutions and approve again")
		}
	}
	principal, grant, err := installationRegistryEntry(snapshot, target.BindingID, params.OrgID)
	if err != nil {
		return nil, err
	}
	if params.Approval != nil && grant.PresenceGeneration != 0 && grant.PresenceGeneration != params.Approval.Generation {
		return nil, InstallationApprovalRefusal("INSTALLATION_APPROVAL_GENERATION_CHANGED", "the authority registry has not reconciled the reviewed generation; refresh the catalogue before approving")
	}
	params.RegistryBindingID, params.RegistryPrincipalID = target.BindingID, principal
	params.RouteAlias, params.Kind = target.RouteAlias, target.Kind
	if params.Kind == "module" && target.Installed && params.OwnerPrincipalID == "" {
		existing, err := s.heldTargetInstallation(ctx, params.OrgID, params.TargetID)
		if err != nil {
			return nil, err
		}
		params.OwnerPrincipalID = existing.GetOwnerPrincipalId()
		if params.CoOwnerPrincipalIDs == nil {
			params.CoOwnerPrincipalIDs = existing.GetCoOwnerPrincipalIds()
		}
	}
	params.ModuleCeiling = nil
	if params.Kind == "module" {
		ceiling := FullInstallationScope(grant)
		params.ModuleCeiling = &ceiling
	}
	if params.Scope == nil {
		recorded := consentedInstallationScope(params.Kind, grant, snapshot.envelopeRevision)
		if params.Kind == "module" && target.Installed {
			// Re-install is the administrator's explicit approval of this
			// generation, even when an envelope exists: record its snapshot.
			recorded = FullInstallationScope(grant)
		}
		params.Scope = &recorded
	}
	if err := validateInstallationScope(*params.Scope, grant); err != nil {
		return nil, NewStoreError(err, ErrTypeValidation)
	}
	params.EnvelopeRevision = snapshot.envelopeRevision
	return snapshot, nil
}

// consentedInstallationScope is what an installation records when its
// administrator installs with no explicit scope: what the organisation
// consented to.
//
// It is the snapshot of the ceiling it was installed under, which a later
// ceiling can only narrow (intersectInstallationScope). A module's installation
// follows its grant instead only where an administrative envelope sets that
// grant (envelopeRevision > 0) — a ceiling a platform administrator, or the
// composition's own signed envelope, approved for every organisation. With no
// envelope the ceiling is the module's signed manifest alone, and a later
// generation that widens it must not reach an organisation no person approved
// it for: the installation keeps its snapshot, and taking the wider generation
// is an explicit lifecycle action by that organisation's administrator
// (calling InstallationService/Install or InstallSolution again).
func consentedInstallationScope(kind string, grant ModulePrincipalGrant, envelopeRevision uint64) InstallationScope {
	if kind == "module" && envelopeRevision > 0 {
		return InstallationScope{Capabilities: true, Follows: true}
	}
	return FullInstallationScope(grant)
}

// NarrowInstallationScope is a human administration act. Authorization belongs
// at the adapter like InstallSolution; the store compares the expected revision
// and the old scope under the row lock. Policy receipt, scope/revision and
// cancellation commit together. A no-op does not advance the revision.
func (s *Service) NarrowInstallationScope(ctx context.Context, actorID, orgID, id string, revision uint64, scope InstallationScope) error {
	store, ok := s.store.(InstallationScopeStore)
	if !ok {
		return status.Error(codes.FailedPrecondition, "installation scope store unavailable")
	}
	snapshot := s.moduleRegistrySnapshot()
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return err
	}
	var current *InstallationAuthorityState
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		current, err = store.InstallationAuthorityState(ctx, orgID, id)
		return err
	}); err != nil {
		return err
	}
	if current.Revision != revision {
		return NewStoreError(fmt.Errorf("installation revision changed"), ErrTypeConflict)
	}
	grant, found := snapshot.registry[current.PrincipalID]
	if !found || current.BindingID != grant.PresenceBinding || !moduleAvailableToOrganization(grant, orgID) ||
		!InstallationScopeWithin(scope, current.Scope) {
		return NewStoreError(fmt.Errorf("scope must narrow this organization's installation"), ErrTypeValidation)
	}
	if err := validateInstallationScope(scope, grant); err != nil {
		return NewStoreError(err, ErrTypeValidation)
	}
	if InstallationScopeWithin(current.Scope, scope) {
		return nil
	}
	// The complete resulting scope is recorded, never only an increment.
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	actorType := s.actorTypeForCreator(ctx, actorID)
	return s.narrow(ctx, func(ctx context.Context) error {
		if err := store.NarrowInstallationScope(ctx, orgID, id, revision, scope, snapshot.registry, snapshot.envelopeRevision); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, actorType, EventInstallationNarrowed,
			"installation", id, orgID, map[string]any{"scope": string(raw), "revision": fmt.Sprint(revision + 1), "binding_id": current.BindingID})
	})
}

// InstallationScopeStore is implemented only by the durable installation owner.
// A narrow interface avoids changing the existing listing/test store contract.
type InstallationScopeStore interface {
	InstallationAuthorityState(context.Context, string, string) (*InstallationAuthorityState, error)
	NarrowInstallationScope(context.Context, string, string, uint64, InstallationScope, ModulePrincipalRegistry, uint64) error
}

// InstallationAuthorityState is read under the organization's transaction. It
// keeps consent distinct from the installation's per-org agent principal.
type InstallationAuthorityState struct {
	Kind, PrincipalID, BindingID string
	Revision                     uint64
	Scope                        InstallationScope
}

// NarrowInstallation resolves the current revision server-side; the store's CAS
// refuses a concurrent lifecycle change instead of overwriting it.
func (s *Service) NarrowInstallation(ctx context.Context, actorID, orgID, id string, scope InstallationScope) error {
	store, ok := s.store.(InstallationScopeStore)
	if !ok {
		return status.Error(codes.FailedPrecondition, "installation scope store unavailable")
	}
	var current *InstallationAuthorityState
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		current, err = store.InstallationAuthorityState(ctx, orgID, id)
		return err
	}); err != nil {
		return err
	}
	return s.NarrowInstallationScope(ctx, actorID, orgID, id, current.Revision, scope)
}
