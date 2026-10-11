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
		available, err = s.store.ListAvailableSolutionTargets(ctx, AvailableSolutionQuery{TargetID: params.TargetID, Limit: 1})
		return err
	}); err != nil {
		return nil, err
	}
	if len(available) != 1 {
		return nil, NewStoreError(ErrSolutionTargetNotInstallable, ErrTypeValidation)
	}
	target := available[0]
	principal, grant, err := installationRegistryEntry(snapshot, target.BindingID, params.OrgID)
	if err != nil {
		return nil, err
	}
	params.RegistryBindingID, params.RegistryPrincipalID = target.BindingID, principal
	params.RouteAlias, params.Kind = target.RouteAlias, target.Kind
	if params.Scope == nil {
		full := FullInstallationScope(grant)
		params.Scope = &full
	}
	if err := validateInstallationScope(*params.Scope, grant); err != nil {
		return nil, NewStoreError(err, ErrTypeValidation)
	}
	params.EnvelopeRevision = snapshot.envelopeRevision
	return snapshot, nil
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
	entry := uninstallPolicyLogEntry(actorID, orgID, id)
	// The complete resulting scope belongs in the log, never only an increment.
	raw, _ := json.Marshal(scope)
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		return err
	}
	entry.OperationID = newPolicyLogOperationID("narrow-installation")
	entry.EnvelopeRevision = snapshot.envelopeRevision
	entry.Policy = map[string]any{"org_id": orgID, "status": "active", "scope": policy, "revision": revision + 1}
	actorType := s.actorTypeForCreator(ctx, actorID)
	return s.WithPolicyLoggedNarrowing(ctx, entry, func(ctx context.Context) error {
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
