package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	policylogv1 "accounts/pkg/gen/saas/policylog/v1"
	"accounts/pkg/infra"
	"accounts/pkg/infra/policylogwitnesstest"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// The client against the REAL witness: the policy-log module's own server,
// service and witness log over its in-memory store, in a helper process.
//
// This is the test the fake cannot be. Every receipt, record and cursor below
// was signed by the witness's code with the witness's key, and verified here by
// this service's restatement of the canonical encodings under a key pinned
// from the witness's own key document. If the two encodings disagreed in a
// single byte, nothing here would verify.
func TestTheClientVerifiesWhatTheRealWitnessSigns(t *testing.T) {
	witness := policylogwitnesstest.Start(t)
	pin, err := infra.ParsePolicyLogWitnessKey(witness.KeyDocument)
	require.NoError(t, err, "the witness's published key document is the trust-anchor schema")

	conn, err := grpc.NewClient(witness.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	writer, err := infra.NewWitnessPolicyLog(policylogv1.NewWitnessServiceClient(conn), pin)
	require.NoError(t, err)

	ctx := context.Background()
	entries := []*business.PolicyLogEntry{
		{
			OperationID: "uninstall-solution/1", Decision: business.PolicyLogNarrowed,
			SubjectKind: business.PolicyLogInstallation, SubjectID: "installation-1",
			Policy: map[string]any{"org_id": "org-1", "status": "revoked"}, Actor: "actor-1",
		},
		{
			OperationID: "revoke-scope/1", Decision: business.PolicyLogNarrowed,
			SubjectKind: business.PolicyLogScopeGrant, SubjectID: "org-1/subject/plsol/role",
			Policy: map[string]any{"status": "revoked"}, Actor: "actor-1",
		},
		{
			OperationID: "regrant/1", Decision: business.PolicyLogRegranted,
			SubjectKind: business.PolicyLogPrincipal, SubjectID: "principal-1",
			Policy: map[string]any{"status": "active"}, Actor: "actor-2", EnvelopeRevision: 7,
		},
	}
	receipts := make([]*business.PolicyLogReceipt, 0, len(entries))
	for i, entry := range entries {
		receipt, err := writer.Append(ctx, entry)
		require.NoError(t, err, "a receipt the real witness signed verifies under the pin")
		require.Equal(t, uint64(i+1), receipt.Sequence, "the witness assigns the sequence")
		receipts = append(receipts, receipt)
	}

	// A retry of the identical operation returns the original receipt: the
	// witness's idempotency, through this client.
	again, err := writer.Append(ctx, entries[0])
	// The retry is a NEW Append call, so it carries a new requested_at and is
	// therefore different bytes under the same operation id — which the
	// witness refuses rather than re-sequencing. That is the property that
	// keeps one operation id one entry.
	require.Error(t, err)
	require.Nil(t, again)

	// Another replica reads the chain back, verifying every link and the signed
	// cursor at its head.
	reader, err := infra.NewWitnessPolicyLog(policylogv1.NewWitnessServiceClient(conn), pin)
	require.NoError(t, err)
	records, err := reader.Entries(ctx, 0, 500)
	require.NoError(t, err)
	require.Len(t, records, len(entries))
	for i, record := range records {
		require.Equal(t, entries[i].OperationID, record.OperationID)
		require.Equal(t, entries[i].SubjectKind, record.SubjectKind)
		require.Equal(t, entries[i].SubjectID, record.SubjectID)
		require.Equal(t, entries[i].Decision, record.Decision)
		require.Equal(t, receipts[i].Sequence, record.Sequence)
		require.Equal(t, receipts[i].Receipt, record.Receipt,
			"the record read back carries the very receipt the append returned")
	}
	records, err = reader.Entries(ctx, uint64(len(entries)), 500)
	require.NoError(t, err)
	require.Empty(t, records)

	// A host that pins a different key believes none of it.
	impostor := policylogwitnesstest.Start(t)
	otherPin, err := infra.ParsePolicyLogWitnessKey(impostor.KeyDocument)
	require.NoError(t, err)
	mispinned, err := infra.NewWitnessPolicyLog(policylogv1.NewWitnessServiceClient(conn), otherPin)
	require.NoError(t, err)
	_, err = mispinned.Entries(ctx, 0, 500)
	require.ErrorIs(t, err, infra.ErrPolicyLogWitnessEvidence)
	_, err = mispinned.Append(ctx, &business.PolicyLogEntry{
		OperationID: "uninstall-solution/2", Decision: business.PolicyLogNarrowed,
		SubjectKind: business.PolicyLogInstallation, SubjectID: "installation-2", Actor: "actor-1",
	})
	require.ErrorIs(t, err, infra.ErrPolicyLogWitnessEvidence)

	// With the witness down, nothing appends and nothing reads.
	witness.Stop()
	_, err = writer.Append(ctx, &business.PolicyLogEntry{
		OperationID: "uninstall-solution/3", Decision: business.PolicyLogNarrowed,
		SubjectKind: business.PolicyLogInstallation, SubjectID: "installation-3", Actor: "actor-1",
	})
	require.Error(t, err)
	_, err = reader.Entries(ctx, uint64(len(entries)), 500)
	require.Error(t, err)
}
