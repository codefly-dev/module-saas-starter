package infra_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// =====================================================================
// actor_chain_journal integration tests (RFC-0003)
// =====================================================================
//
// Real Postgres only. The journal and its revocation list are RLS-protected
// (org-scoped) and append-only; the store methods self-scope via WithOrgTx, so
// tests call them directly.

func newRootHopInput(orgID, ownerID, actorID string) business.ActorChainHopInput {
	return business.ActorChainHopInput{
		ID:               business.NewIDString(),
		OrgID:            orgID,
		TaskID:           "task-" + business.NewIDString(),
		SessionID:        "session-1",
		OwnerPrincipalID: ownerID,
		ActorPrincipalID: actorID,
		ActorKind:        "agent",
		GrantedScopes: []business.ActorChainScope{
			{ResourceKind: "repo", Actions: []string{"read"}},
		},
		AuthorizationRevision: 1,
		HopIndex:              0,
	}
}

func TestActorChainJournal_AppendContentAddressesAndIsLive(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "ci.test/agent:0.1.0")

	in := newRootHopInput(orgID, owner, agent.ID)
	hop, err := testStore.AppendActorChainHop(testCtx, in)
	require.NoError(t, err)
	require.Equal(t, in.ID, hop.ID)
	require.NotEmpty(t, hop.HopHash, "root hop must be content-addressed")
	require.Empty(t, hop.PrevHash, "root hop chains from nothing")
	require.NotEmpty(t, hop.RevocationID)
	require.Equal(t, business.HopContentHash(in, ""), hop.HopHash)

	revoked, err := testStore.AnyActorChainHopRevoked(testCtx, orgID, []string{hop.ID})
	require.NoError(t, err)
	require.False(t, revoked)
}

// A retried append of the SAME hop id is absorbed (crash-safety). This is
// store-level id idempotency, not issuance-level dedup — production always mints
// a fresh hop id per issuance, so this path only guards a retry of one append.
func TestActorChainJournal_AppendSameHopIDIsAbsorbed(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "ci.test/agent:0.1.0")

	in := newRootHopInput(orgID, owner, agent.ID)
	first, err := testStore.AppendActorChainHop(testCtx, in)
	require.NoError(t, err)
	second, err := testStore.AppendActorChainHop(testCtx, in)
	require.NoError(t, err)

	require.Equal(t, first.HopHash, second.HopHash)
	require.Equal(t, first.RevocationID, second.RevocationID,
		"a retried append must not mint a second revocation handle")
}

func TestActorChainJournal_ChildChainsToParentHash(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "ci.test/agent:0.1.0")
	subAgent := seedAgentPrincipal(t, orgID, "ci.test/subagent:0.1.0")

	parent, err := testStore.AppendActorChainHop(testCtx, newRootHopInput(orgID, owner, agent.ID))
	require.NoError(t, err)

	childIn := newRootHopInput(orgID, owner, subAgent.ID)
	childIn.ParentDelegationID = parent.ID
	childIn.HopIndex = 1
	child, err := testStore.AppendActorChainHop(testCtx, childIn)
	require.NoError(t, err)

	require.Equal(t, parent.HopHash, child.PrevHash, "child prev_hash links to parent hop_hash")
	require.NotEqual(t, parent.HopHash, child.HopHash)
}

func TestActorChainJournal_RevokingAncestorKillsDescendant(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "ci.test/agent:0.1.0")
	subAgent := seedAgentPrincipal(t, orgID, "ci.test/subagent:0.1.0")

	parent, err := testStore.AppendActorChainHop(testCtx, newRootHopInput(orgID, owner, agent.ID))
	require.NoError(t, err)
	childIn := newRootHopInput(orgID, owner, subAgent.ID)
	childIn.ParentDelegationID = parent.ID
	childIn.HopIndex = 1
	child, err := testStore.AppendActorChainHop(testCtx, childIn)
	require.NoError(t, err)

	require.NoError(t, testStore.RevokeActorChainHop(testCtx, orgID, parent.ID, owner, "ci revoke"))

	parentRevoked, err := testStore.AnyActorChainHopRevoked(testCtx, orgID, []string{parent.ID})
	require.NoError(t, err)
	require.True(t, parentRevoked)

	childRevoked, err := testStore.AnyActorChainHopRevoked(testCtx, orgID, []string{child.ID})
	require.NoError(t, err)
	require.True(t, childRevoked, "revoking an ancestor kills the descendant that chains through it")
}

func TestActorChainJournal_RevokingLeafSparesAncestor(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "ci.test/agent:0.1.0")
	subAgent := seedAgentPrincipal(t, orgID, "ci.test/subagent:0.1.0")

	parent, err := testStore.AppendActorChainHop(testCtx, newRootHopInput(orgID, owner, agent.ID))
	require.NoError(t, err)
	childIn := newRootHopInput(orgID, owner, subAgent.ID)
	childIn.ParentDelegationID = parent.ID
	childIn.HopIndex = 1
	child, err := testStore.AppendActorChainHop(testCtx, childIn)
	require.NoError(t, err)

	require.NoError(t, testStore.RevokeActorChainHop(testCtx, orgID, child.ID, owner, "ci revoke leaf"))

	childRevoked, err := testStore.AnyActorChainHopRevoked(testCtx, orgID, []string{child.ID})
	require.NoError(t, err)
	require.True(t, childRevoked)

	parentRevoked, err := testStore.AnyActorChainHopRevoked(testCtx, orgID, []string{parent.ID})
	require.NoError(t, err)
	require.False(t, parentRevoked, "revoking a descendant must not revoke its ancestor")
}

func TestActorChainJournal_RevokeUnknownHopFails(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)

	err := testStore.RevokeActorChainHop(testCtx, orgID, business.NewIDString(), owner, "no such hop")
	require.Error(t, err)
	var storeErr *business.StoreError
	require.ErrorAs(t, err, &storeErr)
	require.Equal(t, business.ErrTypeNotFound, storeErr.StoreErrorType)
}

func TestActorChainJournal_LinksToDelegationGrant(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	agent := seedAgentPrincipal(t, orgID, "ci.test/agent:0.1.0")
	grantor := seedAgentPrincipal(t, orgID, "ci.test/grantor:0.1.0")
	grantID := seedActiveGrant(t, orgID, agent.ID, grantor.ID)

	in := newRootHopInput(orgID, owner, agent.ID)
	in.DelegationGrantID = grantID
	hop, err := testStore.AppendActorChainHop(testCtx, in)
	require.NoError(t, err)
	require.Equal(t, grantID, hop.DelegationGrantID)
}

// Revocation must bite on the action path, not only at chain extension. The
// consumer re-validates a token's sealed authorization_revision against current
// facts; a revocation that did not advance that revision would leave the revoked
// hop's token authorizing actions until TTL. Assert the revoke bumps the owner's
// effective revision so the existing epoch check rejects it.
func TestActorChainJournal_RevocationBumpsAuthorizationRevision(t *testing.T) {
	owner := seedUser(t)
	orgID := seedOrg(t, owner)
	seedOrgMember(t, orgID, owner)
	agent := seedAgentPrincipal(t, orgID, "ci.test/agent:0.1.0")

	hop, err := testStore.AppendActorChainHop(testCtx, newRootHopInput(orgID, owner, agent.ID))
	require.NoError(t, err)

	before := readOwnerEffectiveRevision(t, orgID, owner)
	require.NoError(t, testStore.RevokeActorChainHop(testCtx, orgID, hop.ID, owner, "ci revoke"))
	after := readOwnerEffectiveRevision(t, orgID, owner)

	require.Greater(t, after, before,
		"revoking a hop must advance the owner's authorization revision so live tokens go stale")
}

// readOwnerEffectiveRevision returns max(org revision, owner-principal revision)
// — the value a Work Context seals and the consumer re-checks.
func readOwnerEffectiveRevision(t *testing.T, orgID, ownerID string) int64 {
	t.Helper()
	var revision int64
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		return tx.QueryRow(ctx, `
			SELECT GREATEST(
				(SELECT revision FROM organization_authorization_revisions WHERE org_id = $1),
				COALESCE((SELECT revision FROM principal_authorization_revisions
				          WHERE org_id = $1 AND principal_id = $2), 0)
			)`, orgID, ownerID).Scan(&revision)
	}))
	return revision
}

// seedActiveGrant inserts a minimal active delegation grant so a journal hop can
// link to a real delegation_grants.id. delegation_grants is RLS-protected, so the
// insert runs under the org identity.
func seedActiveGrant(t *testing.T, orgID, actorID, grantorID string) string {
	t.Helper()
	id := business.NewIDString()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx, `
			INSERT INTO delegation_grants (
				id, org_id, actor_principal_id, grantor_principal_id,
				action, resource, justification, risk_level, expires_at,
				kind, status, request_hash
			) VALUES ($1, $2, $3, $4,
				'deploy', 'service:api', 'ci grant', 'low',
				CURRENT_TIMESTAMP + INTERVAL '1 hour',
				'one_shot', 'approved', $5)`,
			id, orgID, actorID, grantorID, "grant-"+id)
		return err
	}))
	return id
}
