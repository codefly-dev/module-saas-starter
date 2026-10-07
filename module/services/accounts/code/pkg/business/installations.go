package business

import (
	"context"
	"errors"
	"fmt"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// InstallSolutionParams is the composed install request. The agent principal,
// solution scope node, standing grant, and installation row are created together
// in one transaction; see InstallationStore.InstallSolution.
type InstallSolutionParams struct {
	// InstallerPrincipalID records ownership for bounded nonhuman reconciliation.
	InstallerPrincipalID string
	OrgID                string
	AgentIdentifier      string // "publisher/name:version"
	// TargetID is the immutable solution target being installed — one continuous
	// period of one binding's presence (solution_targets.go). An installer names
	// an identity, never a route alias: an alias is reusable, so installing by
	// alias installs whatever holds it now and whatever takes it later.
	TargetID string
	// RouteAlias is the alias the target currently serves, resolved from the
	// target by the service before the transaction. DISPLAY ONLY — it is the
	// default label of the authority-root scope node and appears in the audit
	// payload. Nothing joins on it and nothing is authorised by it.
	RouteAlias  string
	DisplayName string // empty defaults to AgentIdentifier
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
	// ListInstallations returns one page of an org's installations with their live
	// health, ordered on the keyset the cursor advances. limit is the page size the
	// caller wants PLUS one: the extra row is how the caller detects a further page
	// without a second count query, exactly as ListAccessibleScopes does.
	ListInstallations(ctx context.Context, orgID string, status gen.InstallationStatus, pageToken string, limit int) ([]*gen.InstallationSummary, error)
	// ListCatalogueInstallations reads every organization's active
	// installations for the platform Catalogue (platform_catalogue.go). Unlike
	// the methods above it runs under the control plane, not an org transaction.
	ListCatalogueInstallations(ctx context.Context) ([]*CatalogueInstallationRecord, error)
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
		wool.Field("target_id", params.TargetID))
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
	// The target is resolved before the transaction, for the same reason the
	// actor type is: it is control-plane state, and reading it inside the tenant
	// transaction would reuse that transaction, where the request role holds no
	// grant on the presence relations and the read would return nothing — a
	// denied read printing as "no such target".
	//
	// Resolving is not the enforcement. A tombstone can be applied between this
	// read and the insert, so liveness is enforced in the database by
	// `installations_target_live_on_insert`, where the insert and the target row
	// are in one transaction. What this read is for is the route alias (a display
	// label) and a refusal that names the problem instead of surfacing a trigger.
	alias, err := s.resolveInstallableTarget(ctx, params.TargetID)
	if err != nil {
		return nil, err
	}
	params.RouteAlias = alias
	// The ceiling may only name audiences this host actually serves (issue #952).
	// `allowed_audiences` used to be free text, so an installation could name a
	// consumer that does not exist and then mint for it — the audience is what
	// decides which consumer a capability is good at, so a value nothing serves is
	// a capability pointing nowhere that still passes every other check.
	//
	// Refused HERE as well as narrowed at read, and both are needed: this catches
	// a typo at the moment someone can fix it, and the read-time narrowing catches
	// the set SHRINKING later, when delivery withdraws a solution long after any
	// write.
	if err := s.RequireHostAudiences(ctx, params.AllowedAudiences); err != nil {
		return nil, NewStoreError(err, ErrTypeValidation)
	}
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
				"installation_id":    installation.GetId(),
				"agent_principal_id": installation.GetAgentPrincipalId(),
				"target_id":          params.TargetID,
			}); e != nil {
			return e
		}
		return s.emitTx(ctx, actorID, actorType, EventInstallationCreated,
			"installation", installation.Id, params.OrgID, map[string]any{
				"agent_principal_id": installation.AgentPrincipalId,
				"target_id":          params.TargetID,
				"role_id":            params.RoleID,
				"allowed_audiences":  params.AllowedAudiences,
				"allowed_scopes":     params.AllowedScopes,
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

// listInstallationsDefaultPageSize / …MaxPageSize bound the listing. The max
// mirrors the proto ceiling; the default is generous because the caller this
// exists for — a solution projection — wants an organization's whole installed
// set and would otherwise page for it on every menu read.
const (
	listInstallationsDefaultPageSize = 200
	listInstallationsMaxPageSize     = 500
)

// ListInstallations enumerates one organization's installations with the health
// resolved live beside each, so a caller rendering an installed solution as
// unavailable does not need a GetInstallation per row. Always org-scoped, so it
// runs under WithOrgTx and RLS confines it to that tenant. Cursor-paginated with
// the same over-fetch-one idiom as ListAccessibleScopes.
func (s *Service) ListInstallations(ctx context.Context, req *gen.ListInstallationsRequest) (*gen.ListInstallationsResponse, error) {
	w := wool.Get(ctx).In("ListInstallations", wool.Field("org_id", req.GetOrgId()))
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = listInstallationsDefaultPageSize
	}
	if pageSize > listInstallationsMaxPageSize {
		pageSize = listInstallationsMaxPageSize
	}

	var summaries []*gen.InstallationSummary
	if err := s.store.WithOrgTx(ctx, req.GetOrgId(), func(ctx context.Context) error {
		out, e := s.installationStore().ListInstallations(ctx, req.GetOrgId(), req.GetStatus(), req.GetPageToken(), pageSize+1)
		summaries = out
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list installations")
	}

	var nextToken string
	if len(summaries) > pageSize {
		summaries = summaries[:pageSize]
		// The cursor is the last RETURNED row's key, not the over-fetched row's:
		// the next page must resume at the row after the one the caller saw.
		nextToken = summaries[pageSize-1].GetInstallation().GetId()
	}
	return &gen.ListInstallationsResponse{Installations: summaries, NextPageToken: nextToken}, nil
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
//
// It is a NARROWING, so it runs under the policy log: the entry is appended to
// the external record and its receipt is written here BEFORE anything is
// revoked, and the revocation commits in the same transaction as the receipt's
// commit. A host that cannot witness the append refuses the uninstall rather
// than performing one a restore could silently undo.
//
// The transaction is the policy log's CONTROL-PLANE one rather than this
// organisation's, because the receipt lives in a control-plane relation that
// app_tenant holds no grant on — and the receipt and the revocation have to be
// the same transaction or the protocol's one forbidden state ("applied but
// unwitnessed") becomes reachable. The organisation's own scoping is not lost:
// every statement below names orgID explicitly, which is what the tenant policy
// would have checked.
func (s *Service) UninstallSolution(ctx context.Context, actorID, orgID, installationID string) error {
	w := wool.Get(ctx).In("UninstallSolution", wool.Field("installation_id", installationID))
	actorType := s.actorTypeForCreator(ctx, actorID)
	if err := s.WithPolicyLoggedNarrowing(ctx,
		uninstallPolicyLogEntry(actorID, orgID, installationID),
		func(ctx context.Context) error {
			return s.uninstallSolutionTx(ctx, actorID, actorType, orgID, installationID)
		}); err != nil {
		return w.Wrapf(err, "cannot uninstall solution")
	}
	return nil
}

// uninstallSolutionTx is one uninstall inside the caller's organization
// transaction, shared by an operator's uninstall and an organization's deletion.
func (s *Service) uninstallSolutionTx(ctx context.Context, actorID, actorType, orgID, installationID string) error {
	installation, transitioned, err := s.installationStore().UninstallSolution(ctx, orgID, installationID)
	if err != nil {
		return err
	}
	// Only a real active→revoked transition is a fact worth publishing;
	// an idempotent re-uninstall emits neither event nor audit. Publish in
	// the same transaction (outbox) so the revoke and its event are atomic.
	if !transitioned {
		return nil
	}
	if err := s.publishLifecycleEvent(ctx, EventInstallationRevoked, orgID,
		installation.GetRootScopeNodeId(), actorID, map[string]any{
			"installation_id": installationID,
			"target_id":       installation.GetTargetId(),
		}); err != nil {
		return err
	}
	return s.emitTx(ctx, actorID, actorType, EventInstallationRevoked,
		"installation", installationID, orgID, map[string]any{
			"target_id": installation.GetTargetId(),
		})
}

// ErrSolutionTargetNotInstallable reports that a target cannot be installed: it
// does not exist, it has been withdrawn, or its binding's newest applied
// generation is not a present one.
var ErrSolutionTargetNotInstallable = errors.New("solution target is not installable")

// resolveInstallableTarget confirms a target is live and ACCEPTED, and returns
// the route alias it currently serves.
//
// Accepted means the host has an applied, non-tombstone generation for the
// target's binding. A target whose newest DESIRED generation was refused is
// still installable at the release that WAS applied, which is the correct answer:
// the host is serving that release, and refusing the install would make a bad
// delivery pipeline run look like a withdrawn solution.
//
// It reads through the same catalogue query the listing serves, so there is no
// second predicate that means to agree with it — an administrator cannot install
// something the catalogue would not have offered.
func (s *Service) resolveInstallableTarget(ctx context.Context, targetID string) (string, error) {
	if targetID == "" {
		return "", NewStoreError(
			fmt.Errorf("%w: no target was named", ErrSolutionTargetNotInstallable),
			ErrTypeValidation)
	}
	var alias string
	var found bool
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		// The SAME query the catalogue listing serves, narrowed to one target.
		// One statement, one acceptance predicate: an administrator cannot
		// install something the catalogue would not have offered, and there is
		// no second predicate here that means to agree with that one.
		available, err := s.store.ListAvailableSolutionTargets(ctx,
			AvailableSolutionQuery{TargetID: targetID, Limit: 1})
		if err != nil {
			return err
		}
		if len(available) == 1 {
			alias, found = available[0].RouteAlias, true
		}
		return nil
	}); err != nil {
		return "", err
	}
	if !found {
		return "", NewStoreError(
			fmt.Errorf("%w: %s is withdrawn, unknown, or has no applied present generation",
				ErrSolutionTargetNotInstallable, targetID),
			ErrTypeValidation)
	}
	return alias, nil
}

// ListAvailableSolutions is the catalogue an administrator installs from:
// accepted applied state, never the diagnostic binding listing.
//
// It pages exactly as ListInstallations does — over-fetch one row and use the
// last RETURNED row's key as the cursor — so the two listings an administration
// screen reads side by side behave the same way.
func (s *Service) ListAvailableSolutions(
	ctx context.Context, orgID, pageToken string, pageSize int,
) ([]*AvailableSolutionTarget, string, error) {
	if pageSize <= 0 {
		pageSize = listInstallationsDefaultPageSize
	}
	if pageSize > listInstallationsMaxPageSize {
		pageSize = listInstallationsMaxPageSize
	}
	var available []*AvailableSolutionTarget
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		available, err = s.store.ListAvailableSolutionTargets(ctx,
			AvailableSolutionQuery{OrgID: orgID, Cursor: pageToken, Limit: pageSize + 1})
		return err
	}); err != nil {
		return nil, "", err
	}
	next := ""
	if len(available) > pageSize {
		available = available[:pageSize]
		next = available[pageSize-1].TargetID
	}
	return available, next, nil
}
