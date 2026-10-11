//go:build !pure

package business_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	policylogv1 "accounts/pkg/gen/saas/policylog/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/policylogwitnesstest"
	"accounts/pkg/infra/storetx"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// The policy log on the production path with the REAL witness: real narrowings,
// through the real service, against real PostgreSQL, witnessed by the module's
// own policy-log witness (its server package and witness log over its in-memory
// object store, in a helper process — policylogwitnesstest says why it cannot
// be linked here).
//
// Unlike policy_log_production_path_test.go, nothing on either side is a
// double: the receipt each narrowing commits was sequenced, chained and signed
// by the witness's code, and verified by this host's client under a key pinned
// from the witness's own key document before the narrowing applied.
//
// The serving gate's reconciliation is not driven here. `policy_log_cursor` is
// one row this package shares with the recording log every other test uses, and
// pointing a reconciliation at a second log with its own sequence would — rightly
// — be refused as a rollback. The gate's staleness behaviour is proven against
// that log in the file next door.
func TestTheRealWitnessWitnessesUninstallAndScopeRevocation(t *testing.T) {
	clearData(t)
	ctx := testCtx
	closeEveryGap(t, ctx)
	t.Cleanup(func() { closeEveryGap(t, ctx) })

	witness := policylogwitnesstest.Start(t)
	pin, err := infra.ParsePolicyLogWitnessKey(witness.KeyDocument)
	require.NoError(t, err)
	conn, err := grpc.NewClient(witness.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	witnessLog, err := infra.NewWitnessPolicyLog(policylogv1.NewWitnessServiceClient(conn), pin)
	require.NoError(t, err)

	testService.SetPolicyLog(witnessLog, testStore)
	t.Cleanup(func() { wirePolicyLogForTests(testService, testStore) })

	adminID, orgID := mustUserAndOrg(t, ctx, "plwitness@example.com", "plwitness", "Witness Co")
	memberID, _ := mustUserAndOrg(t, ctx, "plwitnessmember@example.com", "plwitnessmember", "Witness Member Co")
	require.NoError(t, testService.AddOrgMember(ctx, adminID, &gen.AddOrgMemberRequest{
		OrgId: orgID, UserId: memberID, Role: gen.OrgRole_ORG_ROLE_MEMBER,
	}))
	roleID := seedInstallableRole(t, ctx, orgID)
	install := func(label string) string {
		t.Helper()
		targetID, _ := declarePresence(t, label)
		installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
			OrgID:           orgID,
			AgentIdentifier: "acme.example/" + label + ":1.0.0",
			TargetID:        targetID,
			RootScopeLabel:  label,
			RoleID:          roleID,
			AllowedScopes:   []string{"doc"},
		})
		require.NoError(t, err)
		return installation.Id
	}
	grant := func(scopePath string) *gen.RevokeScopeRequest {
		t.Helper()
		_, err := testService.RegisterScopeNode(ctx, adminID, &gen.RegisterScopeNodeRequest{
			OrgId: orgID, ScopePath: scopePath, Kind: business.ScopeNodeKindSolution, Label: scopePath,
		})
		require.NoError(t, err)
		granted, err := testService.GrantScope(ctx, adminID, &gen.GrantScopeRequest{
			OrgId: orgID, SubjectId: memberID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			ScopePath: scopePath, RoleId: roleID,
		})
		require.NoError(t, err)
		return &gen.RevokeScopeRequest{
			OrgId: orgID, SubjectId: memberID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			ScopePath: granted.Grant.ScopePath, RoleId: granted.Grant.RoleId,
		}
	}

	// --- The witness is up: both narrowings succeed, each with its receipt. ---

	installed := install("plwitness-up")
	require.NoError(t, testService.UninstallSolution(ctx, adminID, orgID, installed))
	_, err = testStore.LiveModuleAuthority(ctx, business.ModulePrincipalID("plwitness-up"), orgID, installed)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive, "the uninstall really applied")
	uninstallRow := committedReceipt(t, ctx, business.PolicyLogInstallation, installed)

	revoke := grant("plwitnessup")
	require.Equal(t, 1, scopeGrantCount(t, ctx, orgID, memberID, revoke.ScopePath))
	require.NoError(t, testService.RevokeScope(ctx, adminID, revoke))
	require.Zero(t, scopeGrantCount(t, ctx, orgID, memberID, revoke.ScopePath), "the revocation really applied")
	revokeRow := committedReceipt(t, ctx, business.PolicyLogScopeGrant, orgID+"/")

	// Each committed receipt IS the witness's: a second replica reads the log
	// back, verifying every signature and chain link, and finds both operations
	// at the sequences, and with the receipts, this host committed.
	reader, err := infra.NewWitnessPolicyLog(policylogv1.NewWitnessServiceClient(conn), pin)
	require.NoError(t, err)
	records, err := reader.Entries(ctx, 0, 500)
	require.NoError(t, err)
	require.Len(t, records, 2, "one entry per narrowing, and nothing else")
	for _, row := range []receiptRow{uninstallRow, revokeRow} {
		var found *business.PolicyLogRecord
		for _, record := range records {
			if record.OperationID == row.operationID {
				found = record
			}
		}
		require.NotNil(t, found, "operation %s reached the witness", row.operationID)
		require.Equal(t, row.receipt, found.Receipt, "the stored receipt is the one the witness signed")
		require.Equal(t, row.sequence, found.Sequence)
		requireCommitted(t, ctx, row.operationID)
	}

	// --- The witness is down: both are refused, and nothing narrows. ---

	stillInstalled := install("plwitness-down")
	stillGranted := grant("plwitnessdown")
	witness.Stop()

	err = testService.UninstallSolution(ctx, adminID, orgID, stillInstalled)
	require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)
	_, err = testStore.LiveModuleAuthority(ctx, business.ModulePrincipalID("plwitness-down"), orgID, stillInstalled)
	require.NoError(t, err, "an unwitnessed uninstall must leave the installation live")

	err = testService.RevokeScope(ctx, adminID, stillGranted)
	require.ErrorIs(t, err, business.ErrPolicyLogUnreachable)
	require.Equal(t, 1, scopeGrantCount(t, ctx, orgID, memberID, stillGranted.ScopePath),
		"an unwitnessed revocation must leave the grant in place")

	require.Zero(t, receiptCount(t, ctx, business.PolicyLogInstallation, stillInstalled),
		"a refused append records no receipt, so no gap opens and nothing narrows silently")
	require.Empty(t, uncommittedOperations(t, ctx))
}

type receiptRow struct {
	operationID string
	receipt     string
	sequence    uint64
}

// committedReceipt reads the one committed policy_log_commits row for a subject
// (or, for a subject id ending in "/", the one whose id starts with it).
func committedReceipt(t *testing.T, ctx context.Context, kind business.PolicyLogSubjectKind, subject string) receiptRow {
	t.Helper()
	var row receiptRow
	var sequence int64
	var committedAt *time.Time
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `
			SELECT operation_id, receipt, log_sequence, committed_at
			  FROM policy_log_commits
			 WHERE subject_kind = $1
			   AND (subject_id = $2 OR (right($2, 1) = '/' AND starts_with(subject_id, $2)))`,
			string(kind), subject).Scan(&row.operationID, &row.receipt, &sequence, &committedAt)
	}))
	require.NotNil(t, committedAt, "the receipt committed in the narrowing's transaction")
	row.sequence = uint64(sequence)
	return row
}

func receiptCount(t *testing.T, ctx context.Context, kind business.PolicyLogSubjectKind, subject string) int {
	t.Helper()
	var count int
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx,
			`SELECT count(*) FROM policy_log_commits WHERE subject_kind = $1 AND subject_id = $2`,
			string(kind), subject).Scan(&count)
	}))
	return count
}

func scopeGrantCount(t *testing.T, ctx context.Context, orgID, subjectID, scopePath string) int {
	t.Helper()
	var count int
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx,
			`SELECT count(*) FROM scope_grants WHERE org_id = $1 AND subject_id = $2 AND scope_path::text = $3`,
			orgID, subjectID, scopePath).Scan(&count)
	}))
	return count
}
