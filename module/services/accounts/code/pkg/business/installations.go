package business

import (
	"context"
	"fmt"

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
	// RootScopeLabel is the display label of the kind='solution' node. The node's
	// ltree path is derived server-side from its id (ADR-0002), never caller-chosen.
	RootScopeLabel string
	RoleID         string // the least-privilege role granted at the root node
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
	// ConsumesNamespaces is the set of event namespaces the installed solution
	// declares it consumes from (its manifest `consumes`). At install these are
	// materialized into durable subscriptions for the fresh agent principal from
	// the composed catalog — the compose/install half of the Subscribe grant
	// (EVENTS.md §Subscriptions). Empty means the solution consumes nothing, so
	// no subscription is materialized.
	ConsumesNamespaces []string
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
	// Composing at the store level (one transaction) skips the friendly
	// Principal.Validate() check the CreateAgentPrincipal domain path runs, so a
	// malformed identifier would otherwise surface as an opaque DB CHECK violation
	// mapped to Internal. Validate the shape here at the boundary instead.
	if !looksLikeAgentIdentifier(params.AgentIdentifier) {
		return nil, NewStoreError(
			fmt.Errorf("agent_identifier %q must be 'publisher/name:version'", params.AgentIdentifier),
			ErrTypeValidation,
		)
	}
	// Resolved before the transaction: the lookup runs as System, and calling it
	// inside a tenant transaction would reuse that transaction instead, quietly
	// downgrading an agent actor to "user".
	actorType := s.actorTypeForCreator(ctx, actorID)
	var installation *gen.Installation
	if err := s.store.WithOrgTx(ctx, params.OrgID, func(ctx context.Context) error {
		var e error
		installation, e = s.installationStore().InstallSolution(ctx, params)
		if e != nil {
			return e
		}
		// Publish installation.created in the same transaction (outbox). The
		// boundary is the solution scope node the install just composed.
		if e := s.publishLifecycleEvent(ctx, EventInstallationCreated, params.OrgID,
			installation.GetRootScopeNodeId(), actorID, map[string]any{
				"installation_id":     installation.GetId(),
				"agent_principal_id":  installation.GetAgentPrincipalId(),
				"solution_identifier": params.SolutionIdentifier,
			}); e != nil {
			return e
		}
		return s.emitTx(ctx, actorID, actorType, EventInstallationCreated,
			"installation", installation.Id, params.OrgID, map[string]any{
				"agent_principal_id":  installation.AgentPrincipalId,
				"solution_identifier": params.SolutionIdentifier,
				"role_id":             params.RoleID,
				"allowed_audiences":   params.AllowedAudiences,
				"allowed_scopes":      params.AllowedScopes,
			})
	}); err != nil {
		return nil, w.Wrapf(err, "cannot install solution")
	}
	// Materialize the installed solution's declared consumes into durable
	// subscriptions for its fresh agent principal (EVENTS.md §Subscriptions). This
	// is a control-plane write, so it runs after the tenant install transaction
	// commits rather than inside it; it is idempotent, so a failure here is
	// recovered by a reinstall or a runtime Subscribe and never corrupts the
	// install. A solution that consumes nothing materializes nothing.
	if err := s.MaterializeSubscriptionsFromCatalog(ctx, installation.GetAgentPrincipalId(), actorID, params.ConsumesNamespaces); err != nil {
		w.Warn("cannot materialize installed solution subscriptions", wool.ErrField(err))
	}
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
	actorType := s.actorTypeForCreator(ctx, actorID)
	var installation *gen.Installation
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		installation, e = s.installationStore().TransferInstallationOwnership(ctx, orgID, installationID, newOwnerPrincipalID, coOwnerPrincipalIDs)
		if e != nil {
			return e
		}
		return s.emitTx(ctx, actorID, actorType, EventInstallationOwnershipTransferred,
			"installation", installationID, orgID, map[string]any{
				"owner_principal_id": newOwnerPrincipalID,
			})
	}); err != nil {
		return nil, w.Wrapf(err, "cannot transfer installation ownership")
	}
	return installation, nil
}

// UninstallSolution reverses an install: it revokes the agent principal, removes
// its standing grant, and marks the installation revoked. The solution scope node
// is left in place (inert without a grant or a live agent) so a reinstall reuses
// it. Idempotent on an already-revoked installation (no second audit event).
func (s *Service) UninstallSolution(ctx context.Context, actorID, orgID, installationID string) error {
	w := wool.Get(ctx).In("UninstallSolution", wool.Field("installation_id", installationID))
	actorType := s.actorTypeForCreator(ctx, actorID)
	var installation *gen.Installation
	var transitioned bool
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var e error
		installation, transitioned, e = s.installationStore().UninstallSolution(ctx, orgID, installationID)
		if e != nil {
			return e
		}
		// Only a real active→revoked transition is a fact worth publishing;
		// an idempotent re-uninstall emits neither event nor audit. Publish in
		// the same transaction (outbox) so the revoke and its event are atomic.
		if !transitioned {
			return nil
		}
		if e := s.publishLifecycleEvent(ctx, EventInstallationRevoked, orgID,
			installation.GetRootScopeNodeId(), actorID, map[string]any{
				"installation_id":     installationID,
				"solution_identifier": installation.GetSolutionIdentifier(),
			}); e != nil {
			return e
		}
		return s.emitTx(ctx, actorID, actorType, EventInstallationRevoked,
			"installation", installationID, orgID, map[string]any{
				"solution_identifier": installation.SolutionIdentifier,
			})
	}); err != nil {
		return w.Wrapf(err, "cannot uninstall solution")
	}
	return nil
}
