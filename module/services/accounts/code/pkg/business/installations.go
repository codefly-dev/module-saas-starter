package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// InstallSolutionParams is the composed install request. The agent principal,
// solution scope node, standing grant, and installation row are created together
// in one transaction; see InstallationStore.InstallSolution.
type InstallSolutionParams struct {
	OrgID              string
	AgentIdentifier    string // "publisher/name:version"
	SolutionIdentifier string
	DisplayName        string // empty defaults to AgentIdentifier
	RootScopePath      string // ltree path of the kind='solution' node
	RootScopeLabel     string
	RoleID             string // the least-privilege role granted at the root node
	// OwnerPrincipalID is the accountable human of record; empty defaults to the
	// installing admin (GrantedBy). Must be a current org admin.
	OwnerPrincipalID    string
	CoOwnerPrincipalIDs []string
	// AllowedAudiences / AllowedScopes is the agent's ceiling (migration 108).
	AllowedAudiences []string
	AllowedScopes    []string
	// GrantedBy is the installing admin — the standing grant's grantor and the
	// default owner of record.
	GrantedBy string
}

// InstallationStore is the narrow persistence surface the installation Service
// calls into, mirroring the PrincipalStore pattern so business depends on an
// interface rather than the full PostgresStore. Every method runs inside the
// caller's WithOrgTx transaction (except the headless mint resolution, which is
// on WorkContextAuthorityStore).
type InstallationStore interface {
	InstallSolution(ctx context.Context, params *InstallSolutionParams) (*gen.Installation, error)
	GetInstallation(ctx context.Context, orgID, installationID string) (*gen.Installation, gen.InstallationHealth, error)
	TransferInstallationOwnership(ctx context.Context, orgID, installationID, newOwnerPrincipalID string, coOwnerPrincipalIDs []string) (*gen.Installation, error)
	// The bool reports whether this call actually flipped an active installation
	// to revoked, so the caller emits the audit event exactly once.
	UninstallSolution(ctx context.Context, orgID, installationID string) (*gen.Installation, bool, error)
}

func (s *Service) installationStore() InstallationStore {
	if is, ok := s.store.(InstallationStore); ok {
		return is
	}
	panic("Service.store does not implement InstallationStore; see postgres_installations.go")
}

// InstallSolution composes an installation in one transaction and emits
// installation.created with the grantor, the granted role, and the agent
// principal. The owner of record defaults to the installing admin.
func (s *Service) InstallSolution(ctx context.Context, actorID string, params *InstallSolutionParams) (*gen.Installation, error) {
	w := wool.Get(ctx).In("InstallSolution",
		wool.Field("org_id", params.OrgID),
		wool.Field("solution", params.SolutionIdentifier))
	params.GrantedBy = actorID
	if params.OwnerPrincipalID == "" {
		params.OwnerPrincipalID = actorID
	}
	var installation *gen.Installation
	if err := s.store.WithOrgTx(ctx, params.OrgID, func(ctx context.Context) error {
		var e error
		installation, e = s.installationStore().InstallSolution(ctx, params)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot install solution")
	}
	s.emit(ctx, actorID, s.actorTypeForCreator(ctx, actorID), EventInstallationCreated,
		"installation", installation.Id, params.OrgID, map[string]any{
			"agent_principal_id":  installation.AgentPrincipalId,
			"solution_identifier": params.SolutionIdentifier,
			"role_id":             params.RoleID,
			"allowed_audiences":   params.AllowedAudiences,
			"allowed_scopes":      params.AllowedScopes,
		})
	return installation, nil
}

// GetInstallation returns one installation plus its health, resolved live from
// current authority facts (agent lifecycle, standing grant, owner admin status).
func (s *Service) GetInstallation(ctx context.Context, orgID, installationID string) (*gen.Installation, gen.InstallationHealth, error) {
	w := wool.Get(ctx).In("GetInstallation", wool.Field("installation_id", installationID))
	var installation *gen.Installation
	var health gen.InstallationHealth
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		installation, health, e = s.installationStore().GetInstallation(ctx, orgID, installationID)
		return e
	}); err != nil {
		return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED, w.Wrapf(err, "cannot get installation")
	}
	return installation, health, nil
}

// TransferInstallationOwnership reassigns the owner of record and replaces the
// co-owner succession set. The new owner must be a current org admin; the store
// fails closed otherwise.
func (s *Service) TransferInstallationOwnership(ctx context.Context, actorID, orgID, installationID, newOwnerPrincipalID string, coOwnerPrincipalIDs []string) (*gen.Installation, error) {
	w := wool.Get(ctx).In("TransferInstallationOwnership", wool.Field("installation_id", installationID))
	var installation *gen.Installation
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		installation, e = s.installationStore().TransferInstallationOwnership(ctx, orgID, installationID, newOwnerPrincipalID, coOwnerPrincipalIDs)
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot transfer installation ownership")
	}
	s.emit(ctx, actorID, s.actorTypeForCreator(ctx, actorID), EventInstallationOwnershipTransferred,
		"installation", installationID, orgID, map[string]any{
			"owner_principal_id": newOwnerPrincipalID,
		})
	return installation, nil
}

// UninstallSolution reverses an install: it revokes the agent principal and its
// standing grant, soft-deletes the solution scope node, and marks the
// installation revoked. Idempotent on an already-revoked installation (no second
// audit event).
func (s *Service) UninstallSolution(ctx context.Context, actorID, orgID, installationID string) error {
	w := wool.Get(ctx).In("UninstallSolution", wool.Field("installation_id", installationID))
	var installation *gen.Installation
	var transitioned bool
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		installation, transitioned, e = s.installationStore().UninstallSolution(ctx, orgID, installationID)
		return e
	}); err != nil {
		return w.Wrapf(err, "cannot uninstall solution")
	}
	if transitioned {
		s.emit(ctx, actorID, s.actorTypeForCreator(ctx, actorID), EventInstallationRevoked,
			"installation", installationID, orgID, map[string]any{
				"solution_identifier": installation.SolutionIdentifier,
			})
	}
	return nil
}
