package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// installationColumns is the canonical SELECT list, uuid columns cast to text so
// they scan into strings (matching the gen.Installation wire shape).
// target_id and revoked_reason are nullable — a revoked historical row whose
// target could not be established at the cutover carries neither — so both are
// coalesced here rather than scanned into pointers. An empty target_id reaching
// a reader is therefore the one documented case and not a scan surprise; the
// `installations_active_requires_target` CHECK is what makes it impossible on an
// active row.
const installationColumns = `id::text, org_id::text, agent_principal_id::text,
	owner_principal_id::text, co_owner_principal_ids::text[],
	root_scope_node_id::text, status, created_at, revoked_at,
	COALESCE(target_id::text, ''), COALESCE(revoked_reason, ''), COALESCE(failure_reason, ''), revision`

func scanInstallation(row rowScanner) (*gen.Installation, error) {
	var (
		id, orgID, agentID, ownerID, rootNodeID, status string
		targetID, revokedReason, failureReason          string
		coOwners                                        []string
		createdAt                                       time.Time
		revokedAt                                       *time.Time
		revision                                        uint64
	)
	if err := row.Scan(&id, &orgID, &agentID, &ownerID, &coOwners,
		&rootNodeID, &status, &createdAt, &revokedAt, &targetID, &revokedReason, &failureReason, &revision); err != nil {
		return nil, err
	}
	out := &gen.Installation{
		Id:                  id,
		OrgId:               orgID,
		AgentPrincipalId:    agentID,
		OwnerPrincipalId:    ownerID,
		CoOwnerPrincipalIds: coOwners,
		RootScopeNodeId:     rootNodeID,
		Status:              installationStatusFromColumn(status),
		CreatedAt:           timestamppb.New(createdAt),
		TargetId:            targetID,
		RevokedReason:       revokedReason,
		FailureReason:       failureReason,
		Revision:            revision,
	}
	if revokedAt != nil {
		out.RevokedAt = timestamppb.New(*revokedAt)
	}
	return out, nil
}

func installationStatusFromColumn(status string) gen.InstallationStatus {
	switch status {
	case "pending":
		return gen.InstallationStatus_INSTALLATION_STATUS_PENDING
	case "failed":
		return gen.InstallationStatus_INSTALLATION_STATUS_FAILED
	case "active":
		return gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE
	case "revoked":
		return gen.InstallationStatus_INSTALLATION_STATUS_REVOKED
	default:
		return gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED
	}
}

// isCurrentOrgAdmin reports whether principalID is a current owner/admin member
// of org. The single source of truth for "accountable human is still an admin".
func isCurrentOrgAdmin(ctx context.Context, executor QueryExecutor, orgID, principalID string) (bool, error) {
	var admin bool
	err := executor.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM organization_members
			WHERE org_id = $1 AND user_id = $2 AND role IN ('owner', 'admin')
		)`, orgID, principalID).Scan(&admin)
	return admin, err
}

// firstEligibleOwner returns the first candidate (owner of record, then co-owners
// in order) that is a current org admin, or "" when none is — the fail-closed
// "connection owner left the org" signal. Priority order is preserved so the owner
// of record wins when still eligible; a co-owner is used only as succession.
func firstEligibleOwner(ctx context.Context, executor QueryExecutor, orgID string, candidates []string) (string, error) {
	for _, candidate := range candidates {
		admin, err := isCurrentOrgAdmin(ctx, executor, orgID, candidate)
		if err != nil {
			return "", err
		}
		if admin {
			return candidate, nil
		}
	}
	return "", nil
}

// InstallSolution composes the agent principal, solution scope node, standing
// grant, pending installation and durable step inside the caller's WithOrgTx.
// Only acknowledged consent is usable. An active identical install is idempotent;
// Fresh approval can supersede pending consent; failed work uses retry.
// Composed modules retain their installation path; the callback is solution-owned.
func (s *PostgresStore) installSolution(ctx context.Context, params *business.InstallSolutionParams) (*gen.Installation, error) {
	w := wool.Get(ctx).In("InstallSolution",
		wool.Field("org_id", params.OrgID),
		wool.Field("target_id", params.TargetID))
	executor := s.getQueryExecutor(ctx)
	if params.RegistryPrincipalID != "" {
		if err := checkInstallationEnvelope(ctx, executor, params.EnvelopeRevision); err != nil {
			return nil, err
		}
	}
	if err := lockSolutionInstallation(ctx, executor, params.OrgID, params.TargetID); err != nil {
		return nil, err
	}

	// A module install or re-install is explicit consent by an organization
	// administrator. Check the actor here too: fixture and business callers
	// do not pass through the RPC authorization interceptor.
	if params.Kind == "module" {
		admin, err := isCurrentOrgAdmin(ctx, executor, params.OrgID, params.GrantedBy)
		if err != nil {
			return nil, w.Wrapf(err, "failed to check installing actor org-admin status")
		}
		if !admin {
			return nil, status.Error(codes.PermissionDenied, "only a current organization owner or admin may install or re-install a module to record consent")
		}
	}

	// The owner of record must be a current org admin. The installing caller is
	// already gated as an admin; an explicitly named owner is validated here.
	admin, err := isCurrentOrgAdmin(ctx, executor, params.OrgID, params.OwnerPrincipalID)
	if err != nil {
		return nil, w.Wrapf(err, "failed to check owner org-admin status")
	}
	if !admin {
		return nil, business.NewStoreError(
			fmt.Errorf("owner of record %s is not a current org admin", params.OwnerPrincipalID),
			business.ErrTypeConflict,
		)
	}

	// Reuse the installation, agent and standing grant. For a module, the
	// administrator's explicit re-install consents to the current scope; the
	// existing revision trigger revokes credentials and work under old consent.
	existing, err := scanInstallation(executor.QueryRow(ctx,
		`SELECT `+installationColumns+` FROM installations
		 WHERE org_id = $1 AND target_id = $2::uuid AND status <> 'revoked'`,
		params.OrgID, params.TargetID))
	if err == nil {
		if existing.Status != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE &&
			!(existing.Status == gen.InstallationStatus_INSTALLATION_STATUS_PENDING && params.Approval != nil) {
			return nil, installerConflict("installation already exists; retry a failed installation or uninstall it")
		}
		if e := reconcileExistingInstallation(ctx, executor, existing, params); e != nil {
			return nil, e
		}
		w.Trace("solution already installed; returning existing installation",
			wool.Field("installation_id", existing.Id))
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, w.Wrapf(err, "failed to check for existing installation")
	}

	// 1. Agent principal, idempotent per version.
	agentID, err := s.getOrCreateAgentPrincipal(ctx, params)
	if err != nil {
		return nil, err
	}

	// 2. Solution scope node with a server-derived, UUID-labelled path; reused if a
	// prior revoked install of this solution left one behind.
	nodeID, nodePath, err := s.getOrRegisterSolutionNode(ctx, params)
	if err != nil {
		return nil, err
	}

	// 3. The agent's standing, non-expiring, least-privilege grant at the node.
	grant := &gen.ScopeGrant{
		Id:          business.NewIDString(),
		OrgId:       params.OrgID,
		SubjectId:   agentID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath:   nodePath,
		RoleId:      params.RoleID,
		GrantedBy:   params.GrantedBy,
	}
	if err := s.GrantScope(ctx, grant); err != nil {
		return nil, w.Wrapf(err, "failed to grant standing scope to agent")
	}

	// 4. Persist explicit consent. A direct store call with no scope grants none.
	consent := params.Scope
	if consent == nil {
		consent = &business.InstallationScope{}
	}
	scopeJSON, err := json.Marshal(consent)
	if err != nil {
		return nil, err
	}
	installation, err := scanInstallation(executor.QueryRow(ctx, `
		INSERT INTO installations
			(id, org_id, agent_principal_id, target_id, owner_principal_id,
			 co_owner_principal_ids, root_scope_node_id, status, installer_principal_id, kind, registry_binding_id, registry_principal_id, scope, revision)
		VALUES (gen_random_uuid(), $1, $2, $3::uuid, $4, $5, $6, CASE WHEN $8='module' THEN 'active' ELSE 'pending' END, NULLIF($7, '')::uuid, COALESCE(NULLIF($8,''),'solution'), NULLIF($9,''), NULLIF($10, '')::uuid, $11::jsonb,
 (SELECT COALESCE(max(revision),0)+1 FROM public.installations WHERE org_id=$1 AND target_id=$3::uuid))
		RETURNING `+installationColumns,
		params.OrgID, agentID, params.TargetID, params.OwnerPrincipalID,
		stringArray(params.CoOwnerPrincipalIDs), nodeID, params.InstallerPrincipalID,
		params.Kind, params.RegistryBindingID, params.RegistryPrincipalID, scopeJSON,
	))
	if err != nil {
		// A concurrent install of the same solution won the race between the
		// idempotent-existing check above and this INSERT; the active-solution
		// unique index rejects the second row. Surface it as a retryable conflict
		// (the caller's retry hits the idempotent path) rather than an opaque 500.
		if isUniqueViolation(err) {
			return nil, business.NewStoreError(
				fmt.Errorf("solution target %s is already being installed in org %s", params.TargetID, params.OrgID),
				business.ErrTypeConflict,
			)
		}
		return nil, w.Wrapf(err, "failed to insert installation")
	}
	if installation.Status == gen.InstallationStatus_INSTALLATION_STATUS_PENDING && params.Approval == nil {
		if err := s.EnqueueInstallationStep(ctx, params.OrgID, installation.Id, "initial"); err != nil {
			return nil, err
		}
	}
	return installation, nil
}

func (s *PostgresStore) getOrCreateAgentPrincipal(ctx context.Context, params *business.InstallSolutionParams) (string, error) {
	w := wool.Get(ctx).In("getOrCreateAgentPrincipal")
	existing, err := s.GetAgentPrincipal(ctx, params.OrgID, params.AgentIdentifier)
	if err == nil {
		if existing.AgentIdentifier != params.AgentIdentifier || !sameStringSet(existing.AllowedAudiences, params.AllowedAudiences) || !sameStringSet(existing.AllowedScopes, params.AllowedScopes) || existing.RevokedAt != nil || existing.DisabledAt != nil {
			return "", installerConflict("existing agent revision has incompatible attributes or lifecycle; explicit lifecycle action is required")
		}
		return existing.ID, nil
	}
	var se *business.StoreError
	if !errors.As(err, &se) || se.StoreErrorType != business.ErrTypeNotFound {
		return "", w.Wrapf(err, "failed to look up agent principal")
	}
	displayName := params.DisplayName
	if displayName == "" {
		displayName = params.AgentIdentifier
	}
	agent := &business.Principal{
		ID:               business.NewIDString(),
		Kind:             business.PrincipalKindAgent,
		DisplayName:      displayName,
		OrgID:            params.OrgID,
		AgentIdentifier:  params.AgentIdentifier,
		CreatedBy:        params.GrantedBy,
		CreatedAt:        time.Now().UTC(),
		AllowedAudiences: params.AllowedAudiences,
		AllowedScopes:    params.AllowedScopes,
	}
	if err := s.CreateAgentPrincipal(ctx, agent); err != nil {
		return "", w.Wrapf(err, "failed to create agent principal")
	}
	return agent.ID, nil
}

// getOrRegisterSolutionNode returns the (id, ltree path) of the installation's
// authority-root scope node. On a reinstall it reuses the solution node a prior
// revoked install of this solution left behind (uninstall removes the grant and
// revokes the agent but leaves the node), so the authority root — and any paths
// registered beneath it — stay stable across reinstall. Otherwise it mints a fresh
// node whose path is a depth-1 ltree label derived from the node id (ADR-0002:
// labels are node UUIDs, never caller-chosen strings), which makes collisions and
// nesting between installs impossible.
func (s *PostgresStore) getOrRegisterSolutionNode(ctx context.Context, params *business.InstallSolutionParams) (string, string, error) {
	w := wool.Get(ctx).In("getOrRegisterSolutionNode")
	executor := s.getQueryExecutor(ctx)
	var nodeID, nodePath string
	err := executor.QueryRow(ctx, `
		SELECT n.id::text, n.scope_path::text
		FROM installations i
		JOIN scope_nodes n ON n.id = i.root_scope_node_id
		WHERE i.org_id = $1 AND i.target_id = $2::uuid AND i.status = 'revoked'
		  AND n.kind = 'solution'
		ORDER BY i.revoked_at DESC
		LIMIT 1`,
		params.OrgID, params.TargetID).Scan(&nodeID, &nodePath)
	if err == nil {
		return nodeID, nodePath, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", w.Wrapf(err, "failed to look up prior solution scope node")
	}
	label := params.RootScopeLabel
	if label == "" {
		label = params.RouteAlias
	}
	node := &gen.ScopeNode{
		Id:    business.NewIDString(),
		OrgId: params.OrgID,
		Kind:  "solution",
		Label: label,
	}
	node.ScopePath = strings.ReplaceAll(node.Id, "-", "_")
	if err := s.RegisterScopeNode(ctx, node); err != nil {
		return "", "", w.Wrapf(err, "failed to register solution scope node")
	}
	return node.Id, node.ScopePath, nil
}

// reconcileExistingInstallation preserves identity, ownership and standing
// grants. A module's explicit admin re-install records the newly prepared
// consent; solutions still use their acknowledged installation lifecycle.
func reconcileExistingInstallation(ctx context.Context, executor QueryExecutor, existing *gen.Installation, params *business.InstallSolutionParams) error {
	var installedScope []byte
	var principal, binding, kind string
	if err := executor.QueryRow(ctx, `SELECT scope, COALESCE(registry_principal_id::text,''), registry_binding_id, kind FROM installations WHERE id=$1::uuid AND org_id=$2::uuid`, existing.Id, existing.OrgId).Scan(&installedScope, &principal, &binding, &kind); err != nil {
		return err
	}
	var current business.InstallationScope
	if err := json.Unmarshal(installedScope, &current); err != nil {
		return err
	}
	requested := business.InstallationScope{}
	if params.Scope != nil {
		requested = *params.Scope
	}
	sameAuthority := principal == params.RegistryPrincipalID &&
		(params.RegistryBindingID == "" || binding == params.RegistryBindingID) &&
		(params.Kind == "" || kind == params.Kind)
	sameScope := current.Follows == requested.Follows &&
		business.InstallationScopeWithin(current, requested) && business.InstallationScopeWithin(requested, current)
	var allowedAudiences, allowedScopes []string
	var agentIdentifier, displayName string
	var live bool
	if err := executor.QueryRow(ctx,
		`SELECT allowed_audiences, allowed_scopes, agent_identifier, display_name, revoked_at IS NULL AND disabled_at IS NULL FROM principals WHERE id = $1 AND org_id = $2`,
		existing.AgentPrincipalId, existing.OrgId).Scan(&allowedAudiences, &allowedScopes, &agentIdentifier, &displayName, &live); err != nil {
		return fmt.Errorf("load existing agent ceiling: %w", err)
	}
	// A missing standing grant (an admin revoked it out from under an active
	// install) leaves roleID empty, which mismatches the requested role below and
	// fails closed to a conflict rather than an opaque Internal from ErrNoRows.
	var roleID string
	if err := executor.QueryRow(ctx, `
		SELECT g.role_id::text
		FROM scope_grants g
		JOIN scope_nodes n ON n.id = $2
		WHERE g.org_id = $1 AND g.subject_kind = 'principal'
		  AND g.subject_id = $3 AND g.scope_path = n.scope_path
		LIMIT 1`,
		existing.OrgId, existing.RootScopeNodeId, existing.AgentPrincipalId).Scan(&roleID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load existing standing grant role: %w", err)
	}
	reconsent := kind == "module" && params.Approval != nil && params.ModuleCeiling != nil && !requested.Follows &&
		business.InstallationScopeWithin(requested, *params.ModuleCeiling) &&
		business.InstallationScopeWithin(*params.ModuleCeiling, requested)
	sameAgent := agentIdentifier == params.AgentIdentifier
	if kind == "module" {
		// A new delivered release does not replace the installation's agent.
		// Re-consent updates only scope; publisher/name must still agree.
		installedName, _, _ := strings.Cut(agentIdentifier, ":")
		requestedName, _, _ := strings.Cut(params.AgentIdentifier, ":")
		sameAgent = installedName == requestedName
	}
	if sameAuthority && (reconsent || sameScope) && roleID == params.RoleID && live && sameAgent &&
		existing.OwnerPrincipalId == params.OwnerPrincipalID &&
		(params.DisplayName == "" || params.DisplayName == displayName) &&
		sameStringSet(allowedAudiences, params.AllowedAudiences) &&
		sameStringSet(allowedScopes, params.AllowedScopes) &&
		sameStringSet(existing.CoOwnerPrincipalIds, params.CoOwnerPrincipalIDs) {
		if reconsent {
			encoded, err := json.Marshal(requested)
			if err != nil {
				return err
			}
			_, err = executor.Exec(ctx, `UPDATE public.installations SET scope=$3::jsonb
				WHERE id=$1::uuid AND org_id=$2::uuid AND scope IS DISTINCT FROM $3::jsonb`, existing.Id, existing.OrgId, encoded)
			return err
		}
		return nil
	}
	return business.NewStoreError(
		fmt.Errorf("solution target %s is already installed with different immutable attributes, owner, ceiling, role, or lifecycle; explicit lifecycle action is required",
			params.TargetID),
		business.ErrTypeConflict)
}

// sameStringSet reports whether two string slices hold the same values regardless
// of order. A nil and an empty slice are equal (both mean "unrestricted" for a
// ceiling).
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sortedA := append([]string(nil), a...)
	sortedB := append([]string(nil), b...)
	slices.Sort(sortedA)
	slices.Sort(sortedB)
	return slices.Equal(sortedA, sortedB)
}

// installationStatusColumn is the inverse of installationStatusFromColumn: the
// column value an enum selects, or "" for UNSPECIFIED, which selects every row.
func installationStatusColumn(status gen.InstallationStatus) string {
	switch status {
	case gen.InstallationStatus_INSTALLATION_STATUS_PENDING:
		return "pending"
	case gen.InstallationStatus_INSTALLATION_STATUS_FAILED:
		return "failed"
	case gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE:
		return "active"
	case gen.InstallationStatus_INSTALLATION_STATUS_REVOKED:
		return "revoked"
	default:
		return ""
	}
}

// ListInstallations returns one page of an org's installations with health
// resolved live for each, keyset-paginated on id.
//
// id is the keyset rather than target_id or created_at because it is the
// only column that is unique across the whole table: (org_id, target_id)
// is unique only among ACTIVE rows, so a revoked install of the same solution
// would make that cursor skip or repeat a row, and created_at is not unique at
// all. The cursor is therefore stable and total whatever the status filter is.
//
// RLS confines every read to the caller's tenant; the explicit org_id predicate is
// a second gate on top of that floor, in the same spirit as the scope listing.
func (s *PostgresStore) ListInstallations(
	ctx context.Context, orgID string, status gen.InstallationStatus, pageToken string, limit int,
) ([]*gen.InstallationSummary, error) {
	w := wool.Get(ctx).In("ListInstallations", wool.Field("org_id", orgID))
	executor := s.getQueryExecutor(ctx)

	// A page token is a uuid this store itself emitted. Canonicalize it rather
	// than binding it raw: comparing on id::text means an unparseable cursor
	// simply matches nothing instead of aborting the transaction on a uuid cast
	// (the same trap the scope point check hit), and a uuid has several accepted
	// spellings but one text form.
	cursor := ""
	if pageToken != "" {
		parsed, err := uuid.Parse(pageToken)
		if err != nil {
			return nil, business.NewStoreError(
				fmt.Errorf("page_token %q is not a cursor this listing issued", pageToken),
				business.ErrTypeValidation,
			)
		}
		cursor = parsed.String()
	}

	summaries, err := s.listInstallationSummaries(ctx, executor, orgID,
		installationStatusColumn(status), cursor, limit, "TRUE")
	if err != nil {
		return nil, w.Wrapf(err, "failed to list installations")
	}
	return summaries, nil
}

// installationHealthLabel is resolveInstallationHealth as ONE SQL expression over
// an `installations i` row joined to its agent principal as `hp`, so a listing
// resolves health for every row in the same statement instead of three or four
// round trips per row. The per-row form multiplied every menu read by the number
// of entitled installations (#949 review f4).
//
// It must answer exactly what resolveInstallationHealth answers, in the same
// precedence: a missing or revoked agent, then a disabled one, then no current
// admin among the owner and co-owners, then a missing or expired standing grant.
// TestInstallationListingHealthMatchesGetInstallation holds the two together for
// every state, the way the scope point check and listing are held together.
const installationHealthLabel = `CASE
		WHEN hp.id IS NULL OR hp.revoked_at IS NOT NULL THEN 'agent_revoked'
		WHEN hp.disabled_at IS NOT NULL THEN 'agent_disabled'
		WHEN NOT EXISTS (
			SELECT 1 FROM organization_members hm
			WHERE hm.org_id = i.org_id
			  AND hm.user_id = ANY (ARRAY[i.owner_principal_id] || i.co_owner_principal_ids)
			  AND hm.role IN ('owner', 'admin')
		) THEN 'no_eligible_owner'
		WHEN NOT EXISTS (
			SELECT 1 FROM scope_grants hg
			JOIN scope_nodes hn ON hn.id = i.root_scope_node_id
			WHERE hg.org_id = i.org_id
			  AND hg.subject_kind = 'principal'
			  AND hg.subject_id = i.agent_principal_id
			  AND hg.scope_path @> hn.scope_path
			  AND (hg.expires_at IS NULL OR hg.expires_at > now())
		) THEN 'standing_grant_missing'
		ELSE 'healthy'
	END`

func installationHealthFromLabel(label string) gen.InstallationHealth {
	switch label {
	case "agent_revoked":
		return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_REVOKED
	case "agent_disabled":
		return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_DISABLED
	case "no_eligible_owner":
		return gen.InstallationHealth_INSTALLATION_HEALTH_NO_ELIGIBLE_OWNER
	case "standing_grant_missing":
		return gen.InstallationHealth_INSTALLATION_HEALTH_STANDING_GRANT_MISSING
	case "healthy":
		return gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY
	default:
		return gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED
	}
}

// installationListingColumns is installationColumns qualified by the listing's
// `i` alias, followed by the health label — scanned by scanInstallation plus one.
const installationListingColumns = `i.id::text, i.org_id::text, i.agent_principal_id::text,
	i.owner_principal_id::text, i.co_owner_principal_ids::text[],
	i.root_scope_node_id::text, i.status, i.created_at, i.revoked_at,
	COALESCE(i.target_id::text, ''), COALESCE(i.revoked_reason, ''), COALESCE(i.failure_reason, ''), i.revision, ` + installationHealthLabel

// withHealthLabel scans one listing row: the installation's own columns through
// scanInstallation, so the wire mapping has one definition, then the label.
type withHealthLabel struct {
	row   rowScanner
	label *string
}

func (r withHealthLabel) Scan(dest ...any) error {
	return r.row.Scan(append(dest, r.label)...)
}

// listInstallationSummaries is the one installation listing both reads go through:
// ListInstallations with no narrowing, and ListSolutionEntitlements narrowed to the
// installations a subject may use. predicate is an extra SQL condition over the
// `i` alias whose placeholders are $1..$len(args); the listing's own parameters are
// numbered after them, so a caller's predicate keeps its own fixed numbering.
//
// id is the keyset: it is the only column unique across statuses —
// (org_id, target_id) is unique only among ACTIVE rows, so a revoked
// install of the same solution would make that cursor skip or repeat a row.
func (s *PostgresStore) listInstallationSummaries(
	ctx context.Context, executor QueryExecutor, orgID, statusColumn, cursor string, limit int,
	predicate string, args ...any,
) ([]*gen.InstallationSummary, error) {
	n := len(args)
	org, status, after, limitParam := n+1, n+2, n+3, n+4
	query := fmt.Sprintf(`SELECT %s
		 FROM installations i
		 LEFT JOIN principals hp
		   ON hp.id = i.agent_principal_id AND hp.org_id = i.org_id AND hp.kind = 'agent'
		 WHERE i.org_id = $%d
		   AND ($%d = '' OR i.status = $%d)
		   AND ($%d = '' OR i.id::text > $%d)
		   AND (%s)
		 ORDER BY i.id
		 LIMIT $%d`,
		installationListingColumns, org, status, status, after, after, predicate, limitParam)
	rows, err := executor.Query(ctx, query, append(args, orgID, statusColumn, cursor, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []*gen.InstallationSummary
	for rows.Next() {
		var label string
		installation, e := scanInstallation(withHealthLabel{row: rows, label: &label})
		if e != nil {
			return nil, e
		}
		summaries = append(summaries, &gen.InstallationSummary{
			Installation: installation,
			Health:       installationHealthFromLabel(label),
		})
	}
	return summaries, rows.Err()
}

// GetInstallation loads one installation and resolves its health live from the
// current agent lifecycle, owner admin status, and standing grant.
func (s *PostgresStore) GetInstallation(ctx context.Context, orgID, installationID string) (*gen.Installation, gen.InstallationHealth, error) {
	w := wool.Get(ctx).In("GetInstallation", wool.Field("installation_id", installationID))
	executor := s.getQueryExecutor(ctx)

	installation, err := scanInstallation(executor.QueryRow(ctx,
		`SELECT `+installationColumns+` FROM installations WHERE id = $1 AND org_id = $2`,
		installationID, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED,
				business.NewStoreError(fmt.Errorf("installation %s not found", installationID), business.ErrTypeNotFound)
		}
		return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED, w.Wrapf(err, "failed to get installation")
	}

	health, err := s.resolveInstallationHealth(ctx, executor, installation)
	if err != nil {
		return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED, err
	}
	return installation, health, nil
}

func (s *PostgresStore) resolveInstallationHealth(ctx context.Context, executor QueryExecutor, installation *gen.Installation) (gen.InstallationHealth, error) {
	var revoked, disabled bool
	err := executor.QueryRow(ctx, `
		SELECT revoked_at IS NOT NULL, disabled_at IS NOT NULL
		FROM principals WHERE id = $1 AND org_id = $2 AND kind = 'agent'`,
		installation.AgentPrincipalId, installation.OrgId).Scan(&revoked, &disabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_REVOKED, nil
		}
		return 0, fmt.Errorf("resolve installation agent health: %w", err)
	}
	switch {
	case revoked:
		return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_REVOKED, nil
	case disabled:
		return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_DISABLED, nil
	}

	owner, err := firstEligibleOwner(ctx, executor, installation.OrgId,
		append([]string{installation.OwnerPrincipalId}, installation.CoOwnerPrincipalIds...))
	if err != nil {
		return 0, fmt.Errorf("resolve installation owner health: %w", err)
	}
	if owner == "" {
		return gen.InstallationHealth_INSTALLATION_HEALTH_NO_ELIGIBLE_OWNER, nil
	}

	var hasGrant bool
	if err := executor.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM scope_grants g
			JOIN scope_nodes n ON n.id = $3
			WHERE g.org_id = $1
			  AND g.subject_kind = 'principal'
			  AND g.subject_id = $2
			  AND g.scope_path @> n.scope_path
			  AND (g.expires_at IS NULL OR g.expires_at > now())
		)`,
		installation.OrgId, installation.AgentPrincipalId, installation.RootScopeNodeId,
	).Scan(&hasGrant); err != nil {
		return 0, fmt.Errorf("resolve installation grant health: %w", err)
	}
	if !hasGrant {
		return gen.InstallationHealth_INSTALLATION_HEALTH_STANDING_GRANT_MISSING, nil
	}
	return gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, nil
}

// TransferInstallationOwnership reassigns the owner of record and replaces the
// co-owner set. The new owner must be a current org admin.
func (s *PostgresStore) TransferInstallationOwnership(ctx context.Context, orgID, installationID, newOwnerPrincipalID string, coOwnerPrincipalIDs []string) (*gen.Installation, error) {
	w := wool.Get(ctx).In("TransferInstallationOwnership", wool.Field("installation_id", installationID))
	executor := s.getQueryExecutor(ctx)

	admin, err := isCurrentOrgAdmin(ctx, executor, orgID, newOwnerPrincipalID)
	if err != nil {
		return nil, w.Wrapf(err, "failed to check new owner org-admin status")
	}
	if !admin {
		return nil, business.NewStoreError(
			fmt.Errorf("new owner %s is not a current org admin", newOwnerPrincipalID),
			business.ErrTypeConflict,
		)
	}

	installation, err := scanInstallation(executor.QueryRow(ctx, `
		UPDATE installations
		SET owner_principal_id = $3, co_owner_principal_ids = $4
		WHERE id = $1 AND org_id = $2 AND status = 'active'
		RETURNING `+installationColumns,
		installationID, orgID, newOwnerPrincipalID, stringArray(coOwnerPrincipalIDs),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(
				fmt.Errorf("active installation %s not found", installationID), business.ErrTypeNotFound)
		}
		return nil, w.Wrapf(err, "failed to transfer installation ownership")
	}
	return installation, nil
}

// UninstallSolution soft-deletes the installation and reverses the composition:
// it removes the agent's standing grant and revokes the agent principal. The
// solution scope node is left in place (inert without a grant or a live agent) so
// a reinstall reuses it. Idempotent on an already-revoked installation.
func (s *PostgresStore) UninstallSolution(ctx context.Context, orgID, installationID string) (*gen.Installation, bool, error) {
	w := wool.Get(ctx).In("UninstallSolution", wool.Field("installation_id", installationID))
	executor := s.getQueryExecutor(ctx)

	// The installation row is locked FIRST, before any grant is touched. An
	// exposure grant locks the same row (FOR SHARE, InstallationRootScopePath)
	// before it writes, and both writes fire triggers that lock the
	// organization's revision row. Taking the installation lock first on both
	// paths is what keeps that a single lock order rather than a cycle: whichever
	// transaction holds the installation row finishes before the other touches a
	// grant.
	current, err := scanInstallation(executor.QueryRow(ctx,
		`SELECT `+installationColumns+` FROM installations WHERE id = $1 AND org_id = $2 FOR UPDATE`,
		installationID, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, business.NewStoreError(
				fmt.Errorf("installation %s not found", installationID), business.ErrTypeNotFound)
		}
		return nil, false, w.Wrapf(err, "failed to load installation")
	}
	if current.Status == gen.InstallationStatus_INSTALLATION_STATUS_REVOKED {
		return current, false, nil
	}

	// Remove the agent's standing grant, then revoke the agent principal. The
	// solution scope node is left in place: the soft-deleted installation row
	// still references it, and with no grant and a revoked agent the node is inert
	// (CheckAccess resolves nothing through it). Order is arbitrary within the tx.
	if _, err := executor.Exec(ctx, `
		DELETE FROM scope_grants
		WHERE org_id = $1 AND subject_kind = 'principal' AND subject_id = $2
		  AND scope_path = (SELECT scope_path FROM scope_nodes WHERE id = $3)`,
		orgID, current.AgentPrincipalId, current.RootScopeNodeId,
	); err != nil {
		return nil, false, w.Wrapf(err, "failed to revoke standing grant")
	}
	if _, err := executor.Exec(ctx, `
		UPDATE principals SET revoked_at = CURRENT_TIMESTAMP, revoked_reason = 'installation uninstalled'
		WHERE id = $1 AND org_id = $2 AND kind = 'agent' AND revoked_at IS NULL`,
		current.AgentPrincipalId, orgID,
	); err != nil {
		return nil, false, w.Wrapf(err, "failed to revoke agent principal")
	}

	revoked, err := scanInstallation(executor.QueryRow(ctx, `
		UPDATE installations SET status = 'revoked', revoked_at = CURRENT_TIMESTAMP, failure_reason = NULL
		WHERE id = $1 AND org_id = $2 AND status <> 'revoked'
		RETURNING `+installationColumns,
		installationID, orgID,
	))
	if err != nil {
		return nil, false, w.Wrapf(err, "failed to mark installation revoked")
	}
	return revoked, true, nil
}

// ResolveInstallationAuthority is the headless-mint authority resolution. It runs
// under the control plane because the caller is a service credential with no user
// identity; every query is explicitly org-scoped, so cross-tenant reach is
// impossible despite the bypassed RLS floor.
func (s *PostgresStore) ResolveInstallationAuthority(ctx context.Context, orgID, installationID string, permissions []business.WorkContextPermission) (*business.InstallationAuthorityFacts, error) {
	var facts *business.InstallationAuthorityFacts
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		var resolveErr error
		facts, resolveErr = resolveInstallationAuthority(ctx, s.getQueryExecutor(ctx), orgID, installationID, permissions, false)
		return resolveErr
	})
	if err != nil {
		return nil, err
	}
	return facts, nil
}

func resolveInstallationAuthority(ctx context.Context, executor QueryExecutor, orgID, installationID string, permissions []business.WorkContextPermission, installationStep bool) (*business.InstallationAuthorityFacts, error) {
	var (
		agentID, ownerID, rootNodeID, rootScopePath string
		coOwners                                    []string
	)
	err := executor.QueryRow(ctx, `
		SELECT i.agent_principal_id::text, i.owner_principal_id::text,
		       i.co_owner_principal_ids::text[], i.root_scope_node_id::text, n.scope_path::text
		FROM installations i
		JOIN scope_nodes n ON n.id = i.root_scope_node_id
		WHERE i.id = $1 AND i.org_id = $2 AND (i.status = 'active' OR ($3 AND i.status = 'pending'))`,
		installationID, orgID, installationStep,
	).Scan(&agentID, &ownerID, &coOwners, &rootNodeID, &rootScopePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.NewStoreError(
			fmt.Errorf("active installation %s not found", installationID), business.ErrTypeNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve installation: %w", err)
	}

	// Agent actor, fail-closed on revoke/disable — the same filter the delegated
	// mint applies in resolveWorkContextAuthority.
	var actor business.Principal
	var orgIDOut, agentIdentifier, createdBy *string
	err = executor.QueryRow(ctx, `
		SELECT id::text, kind, display_name, org_id::text, agent_identifier,
		       created_at, created_by::text, allowed_audiences, allowed_scopes
		FROM principals
		WHERE id = $1 AND org_id = $2 AND kind = 'agent'
		  AND revoked_at IS NULL AND disabled_at IS NULL`,
		agentID, orgID,
	).Scan(&actor.ID, &actor.Kind, &actor.DisplayName, &orgIDOut, &agentIdentifier,
		&actor.CreatedAt, &createdBy, &actor.AllowedAudiences, &actor.AllowedScopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.NewStoreError(
			errors.New("installation agent principal is revoked or disabled"), business.ErrTypeNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve installation actor: %w", err)
	}
	if orgIDOut != nil {
		actor.OrgID = *orgIDOut
	}
	if agentIdentifier != nil {
		actor.AgentIdentifier = *agentIdentifier
	}
	if createdBy != nil {
		actor.CreatedBy = *createdBy
	}

	// Owner of record, resolved live and fail-closed: none of the declared
	// owner/co-owners is currently an org admin.
	ownerOfRecord, err := firstEligibleOwner(ctx, executor, orgID,
		append([]string{ownerID}, coOwners...))
	if err != nil {
		return nil, fmt.Errorf("resolve installation owner of record: %w", err)
	}
	if ownerOfRecord == "" {
		return nil, business.NewStoreError(
			errors.New("no owner or co-owner of the installation is currently an org admin"),
			business.ErrTypeNotFound)
	}

	facts := &business.InstallationAuthorityFacts{OwnerPrincipalID: ownerOfRecord, Actor: &actor}
	var orgRevision, ownerRevision int64
	var teamIDs []string
	err = executor.QueryRow(ctx, `
		SELECT organization_revision.revision,
		       principal_revision.revision,
		       COALESCE(
		           array_agg(team.id::text ORDER BY team.id)
		               FILTER (WHERE team.id IS NOT NULL),
		           ARRAY[]::text[]
		       )
		FROM organization_authorization_revisions AS organization_revision
		JOIN principal_authorization_revisions AS principal_revision
		  ON principal_revision.org_id = organization_revision.org_id
		 AND principal_revision.principal_id = $2
		LEFT JOIN team_members AS team_membership
		  ON team_membership.user_id = $2
		LEFT JOIN teams AS team
		  ON team.id = team_membership.team_id
		 AND team.org_id = organization_revision.org_id
		WHERE organization_revision.org_id = $1
		GROUP BY organization_revision.revision, principal_revision.revision`,
		orgID, ownerOfRecord,
	).Scan(&orgRevision, &ownerRevision, &teamIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve installation owner revision: %w", err)
	}
	if orgRevision <= 0 || ownerRevision <= 0 {
		return nil, errors.New("authorization revision must be positive")
	}
	facts.OrganizationRevision = uint64(orgRevision)
	facts.OwnerPrincipalRevision = uint64(ownerRevision)
	facts.AttributionTeamIDs = append([]string(nil), teamIDs...)

	// Every requested scope must fall within the agent's STANDING grant, resolved
	// hierarchically against the scope tree. A scoped request names a boundary node
	// (resource_id); an unscoped one is checked against the installation root.
	for _, permission := range permissions {
		allowed, err := installationScopeAllowed(ctx, executor, orgID, agentID, rootScopePath, permission)
		if err != nil {
			return nil, fmt.Errorf("check installation standing grant: %w", err)
		}
		if !allowed {
			return nil, business.NewStoreError(
				fmt.Errorf("agent standing grant does not authorize %s:%s at requested scope",
					permission.ResourceKind, permission.Action),
				business.ErrTypePermission)
		}
	}
	return facts, nil
}

// installationScopeAllowed resolves whether the agent's standing scope grant
// authorizes one requested (kind, action) at a boundary. The boundary's scope
// path is resolved from its own registered node (resource_id) — never trusted
// from the request — and must be a descendant-or-equal of the installation's own
// root; the grant must then sit at an ancestor-or-equal of the boundary, with a
// role permitting the (kind, action). Bounding the boundary by the root is what
// keeps a grant the agent later receives elsewhere (a record share, another
// install's node, an admin's ad-hoc GrantScope) from widening what this headless
// path may mint for. An unscoped request (empty ResourceID) resolves against the
// installation's own root path.
func installationScopeAllowed(ctx context.Context, executor QueryExecutor, orgID, agentID, rootScopePath string, permission business.WorkContextPermission) (bool, error) {
	var allowed bool
	err := executor.QueryRow(ctx, `
		WITH boundary AS (
			SELECT CASE
				WHEN $5 = '' THEN $6::ltree
				ELSE (SELECT scope_path FROM scope_nodes WHERE org_id = $1 AND id::text = $5)
			END AS scope_path
		)
		SELECT EXISTS (
			SELECT 1
			FROM scope_grants g
			JOIN role_permissions rp ON rp.role_id = g.role_id
			CROSS JOIN boundary
			WHERE g.org_id = $1
			  AND g.subject_kind = 'principal'
			  AND g.subject_id = $2
			  AND boundary.scope_path IS NOT NULL
			  AND boundary.scope_path <@ $6::ltree
			  AND g.scope_path @> boundary.scope_path
			  AND (g.expires_at IS NULL OR g.expires_at > now())
			  AND (rp.resource = '*' OR rp.resource = $3)
			  AND (rp.action = '*' OR rp.action = $4)
		)`,
		orgID, agentID, permission.ResourceKind, permission.Action, permission.ResourceID, rootScopePath,
	).Scan(&allowed)
	return allowed, err
}

// ListCatalogueInstallations lists every active installation in every
// organization for the platform Catalogue, with the organization's name, the
// identifier of the agent principal it was installed as, and the active team
// grants at its authority root (granted there) or above it (inherited). It must
// run under the control plane: it reads across tenants.
func (s *PostgresStore) ListCatalogueInstallations(ctx context.Context) ([]*business.CatalogueInstallationRecord, error) {
	executor := s.getQueryExecutor(ctx)
	rows, err := executor.Query(ctx, `SELECT `+installationColumns+`, org_name, agent_identifier
		FROM (SELECT i.*, o.name AS org_name, COALESCE(p.agent_identifier, '') AS agent_identifier
		        FROM installations i
		        JOIN organizations o ON o.id = i.org_id
		        LEFT JOIN principals p ON p.id = i.agent_principal_id
		       WHERE i.status = 'active') installations
		-- target_id, because migration 23 DROPPED solution_identifier. An
		-- installation names the immutable target now, and ordering by a
		-- column that no longer exists is a hard query error on every fresh
		-- database — which is what CI installs, and what a worktree whose
		-- pgdata predates the migration does not.
		ORDER BY target_id, org_name, id`)
	if err != nil {
		return nil, fmt.Errorf("list catalogue installations: %w", err)
	}
	var records []*business.CatalogueInstallationRecord
	byID := make(map[string]*business.CatalogueInstallationRecord)
	for rows.Next() {
		record := &business.CatalogueInstallationRecord{}
		installation, err := scanInstallation(trailingColumns{rows, []any{&record.OrgName, &record.AgentIdentifier}})
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan catalogue installation: %w", err)
		}
		record.Installation = installation
		records = append(records, record)
		byID[installation.Id] = record
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("list catalogue installations: %w", err)
	}

	if err := installationTeamGrants(ctx, executor, "TRUE",
		func(installationID, orgID string, atRoot bool, view *gen.CollectionReadGrant) {
			view.Grant.OrgId = orgID
			record, ok := byID[installationID]
			switch {
			case !ok:
			case atRoot:
				record.GrantedTeams = append(record.GrantedTeams, view)
			default:
				record.InheritedTeams = append(record.InheritedTeams, view)
			}
		}); err != nil {
		return nil, fmt.Errorf("list catalogue installation teams: %w", err)
	}
	return records, nil
}

// installationTeamGrants reads every active team grant that reaches an active
// installation's authority root — granted at that node (atRoot) or inherited
// from a grant above it — narrowed by predicate over `i`. It is the one reading
// of "which teams reach this installation", shared by the platform Catalogue
// (every organization, under the control plane) and an organization's own
// listing (one organization, under its tenant transaction), so the two can
// never disagree about what a team reaches.
func installationTeamGrants(
	ctx context.Context, executor QueryExecutor, predicate string,
	each func(installationID, orgID string, atRoot bool, view *gen.CollectionReadGrant),
	args ...any,
) error {
	grants, err := executor.Query(ctx, `SELECT i.id::text, i.org_id::text, g.scope_path = root.scope_path, `+labelledGrantColumns+`
		FROM installations i
		JOIN scope_nodes root ON root.id = i.root_scope_node_id
		JOIN scope_grants g ON g.org_id = i.org_id AND g.subject_kind = 'team'
		 AND g.scope_path @> root.scope_path
		 AND (g.expires_at IS NULL OR g.expires_at > NOW())
		`+labelledGrantJoins+`
		WHERE i.status = 'active' AND (`+predicate+`)
		ORDER BY i.id, g.created_at, g.id`, args...)
	if err != nil {
		return err
	}
	defer grants.Close()
	for grants.Next() {
		var installationID, orgID string
		var atRoot bool
		view, err := scanLabelledGrant(grants, "", &installationID, &orgID, &atRoot)
		if err != nil {
			return fmt.Errorf("scan installation team: %w", err)
		}
		each(installationID, orgID, atRoot, view)
	}
	return grants.Err()
}

// InstallationRootScopePath resolves an ACTIVE installation of orgID to the
// scope path of its authority-root node — where a grant that exposes it is
// written. Run under the organization's transaction: RLS confines the read to
// that tenant, and the explicit org_id predicate is a second gate on top of it,
// so another organization's installation id resolves to not found, never to a
// path. A revoked installation is not found either: exposing an uninstalled
// solution would grant reach into a node nothing is installed at.
//
// It takes an UPDATE lock on the installation row, held until the caller's
// transaction ends. Exposure changes its revision in this transaction too,
// so an UPDATE lock prevents concurrent grants from deadlocking on upgrade.
// UninstallSolution flips the row to revoked with an UPDATE,
// which that lock blocks, so the two serialize on the row: an uninstall that
// commits first leaves the row revoked, and this read — waiting on it, then
// re-checking `status = 'active'` against the committed row — finds nothing; a
// read that locks first holds the uninstall off until the caller commits.
func (s *PostgresStore) InstallationRootScopePath(ctx context.Context, orgID, installationID string) (string, error) {
	var path string
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT n.scope_path::text
		  FROM installations i
		  JOIN scope_nodes n ON n.id = i.root_scope_node_id
		 WHERE i.org_id = $1 AND i.id::text = $2 AND i.status = 'active'
		   FOR UPDATE OF i`,
		orgID, installationID).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", business.NewStoreError(
			fmt.Errorf("installation %s is not an active installation in this organization", installationID),
			business.ErrTypeNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("resolve installation root scope: %w", err)
	}
	return path, nil
}

// ListInstallationExposure returns, for each of orgID's active installations in
// installationIDs, its authority-root scope path and the team grants that reach
// it there. Run under the organization's transaction.
func (s *PostgresStore) ListInstallationExposure(
	ctx context.Context, orgID string, installationIDs []string,
) (map[string]*business.InstallationExposure, error) {
	out := make(map[string]*business.InstallationExposure, len(installationIDs))
	if len(installationIDs) == 0 {
		return out, nil
	}
	executor := s.getQueryExecutor(ctx)
	rows, err := executor.Query(ctx, `
		SELECT i.id::text, n.scope_path::text
		  FROM installations i
		  JOIN scope_nodes n ON n.id = i.root_scope_node_id
		 WHERE i.org_id = $1 AND i.status = 'active' AND i.id::text = ANY($2::text[])`,
		orgID, installationIDs)
	if err != nil {
		return nil, fmt.Errorf("list installation roots: %w", err)
	}
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan installation root: %w", err)
		}
		out[id] = &business.InstallationExposure{RootScopePath: path}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("list installation roots: %w", err)
	}
	if err := installationTeamGrants(ctx, executor, "i.org_id = $1 AND i.id::text = ANY($2::text[])",
		func(installationID, _ string, atRoot bool, view *gen.CollectionReadGrant) {
			view.Grant.OrgId = orgID
			exposure, ok := out[installationID]
			switch {
			case !ok:
			case atRoot:
				exposure.GrantedTeams = append(exposure.GrantedTeams, view)
			default:
				exposure.InheritedTeams = append(exposure.InheritedTeams, view)
			}
		}, orgID, installationIDs); err != nil {
		return nil, fmt.Errorf("list installation teams: %w", err)
	}
	return out, nil
}

// RolePermits reports whether roleID — a role visible to the caller's tenant —
// permits (resource, action), directly or through a wildcard, read through the
// same role_permissions match every authority check uses.
//
// One pair of the set RolePermittedScopes answers for, so that the pair this
// asks about and the set a derived role is built from can never be matched by
// two predicates that mean to agree (postgres_derived_module_access.go).
func (s *PostgresStore) RolePermits(ctx context.Context, roleID, resource, action string) (bool, error) {
	permitted, err := s.RolePermittedScopes(ctx, roleID, []string{resource}, []string{action})
	if err != nil {
		return false, err
	}
	return len(permitted) == 1 && permitted[0], nil
}

// trailingColumns scans columns a query selects after a canonical column list
// into extra destinations, so the canonical scanner stays the only reader of it.
type trailingColumns struct {
	rowScanner
	extra []any
}

func (t trailingColumns) Scan(dest ...any) error {
	return t.rowScanner.Scan(append(dest, t.extra...)...)
}

var _ business.InstallationStore = (*PostgresStore)(nil)

// InstallationRoleRevision reads the standing grant at this installation's own
// root, including pending consent. It never chooses among ambiguous roles.
func (s *PostgresStore) InstallationRoleRevision(ctx context.Context, org, id string) (string, string, uint64, error) {
	var roleID, role string
	var revision uint64
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
 SELECT r.id::text, r.name, i.revision
 FROM installations i JOIN scope_nodes n ON n.id=i.root_scope_node_id
 JOIN scope_grants g ON g.org_id=i.org_id AND g.subject_kind='principal'
   AND g.subject_id=i.agent_principal_id AND g.scope_path=n.scope_path
 JOIN roles r ON r.id=g.role_id
 WHERE i.org_id=$1 AND i.id=$2 AND i.status <> 'revoked'
 AND 1 = (SELECT count(*) FROM scope_grants own WHERE own.org_id=i.org_id AND own.subject_kind='principal' AND own.subject_id=i.agent_principal_id AND own.scope_path=n.scope_path)`, org, id).Scan(&roleID, &role, &revision)
	if err != nil {
		return "", "", 0, fmt.Errorf("installation %s standing role and revision: %w", id, err)
	}
	return roleID, role, revision, nil
}

// ActivateInstallationFollowingCore activates accounts' record of an
// installation whose authority core installation is active: pending its
// install step, or failed at it, it becomes active, keeping the root scope
// node it was installed with. It reports whether it changed the row; an
// active or revoked installation is left as it is. The installation-step
// projection (record_installation_step) only ever moves a pending row, so a
// later acknowledgement changes nothing.
func (s *PostgresStore) ActivateInstallationFollowingCore(ctx context.Context, orgID, installationID string) (bool, error) {
	tag, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE installations SET status = 'active', failure_reason = NULL
		 WHERE org_id = $1 AND id::text = $2 AND status IN ('pending', 'failed')`,
		orgID, installationID)
	if err != nil {
		return false, fmt.Errorf("activate installation %s following the authority core: %w", installationID, err)
	}
	return tag.RowsAffected() == 1, nil
}
