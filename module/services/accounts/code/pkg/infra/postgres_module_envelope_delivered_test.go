//go:build !pure

package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Migration 42 on a real database: a module envelope row is a user's or a
// delivery's, never both and never neither; a delivered revision may skip
// ahead of the committed one and reads back with its signature; the table is
// still append-only over the revision.
func TestAModuleEnvelopeRowIsWrittenByAUserOrDelivered(t *testing.T) {
	ctx := testCtx
	var current *business.ModuleEnvelopeRecord
	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		var err error
		current, err = testStore.LatestModuleEnvelopeRecord(ctx)
		return err
	}))
	// The next rows keep whatever ceiling the shared database holds, so no
	// other test reads a narrowing it did not write.
	base := map[string]any{"principals": map[string]any{}}
	var committed uint64
	if current != nil {
		committed = current.Revision
		require.NoError(t, json.Unmarshal(current.Document, &base))
	}
	document := func(revision uint64) []byte {
		base["revision"] = revision
		raw, err := json.Marshal(base)
		require.NoError(t, err)
		return raw
	}
	appendRecord := func(record *business.ModuleEnvelopeRecord, expected uint64) error {
		return testStore.WithControlPlane(ctx, func(ctx context.Context) error {
			return testStore.AppendModuleEnvelopeRecord(ctx, record, expected)
		})
	}
	checkViolation := func(err error) {
		t.Helper()
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "want a check violation, got %v", err)
		require.Equal(t, "23514", pgErr.Code)
	}
	const admin = "019f6bf7-5b4b-74e5-8c17-0000000000a1"
	delivered := func(revision uint64) *business.ModuleEnvelopeRecord {
		return &business.ModuleEnvelopeRecord{Revision: revision, Document: document(revision),
			Signer: "example-release", OwnershipDomain: "acme", Payload: []byte(`{"schema":"x"}`),
			Envelope: []byte(`{"payloadType":"x","payload":"e30=","signatures":[]}`)}
	}

	both := delivered(committed + 1)
	both.WrittenBy = admin
	checkViolation(appendRecord(both, committed))
	partial := delivered(committed + 1)
	partial.Envelope = nil
	checkViolation(appendRecord(partial, committed))
	checkViolation(appendRecord(&business.ModuleEnvelopeRecord{Revision: committed + 1, Document: document(committed + 1)}, committed))

	// A delivery may skip revisions; it never lands at or below the committed one.
	skipped := delivered(committed + 3)
	require.NoError(t, appendRecord(skipped, committed))
	require.ErrorIs(t, appendRecord(delivered(committed+3), committed+3), business.ErrModuleEnvelopeConflict)
	require.ErrorIs(t, appendRecord(delivered(committed+4), committed), business.ErrModuleEnvelopeConflict)
	require.NoError(t, appendRecord(&business.ModuleEnvelopeRecord{Revision: committed + 4, Document: document(committed + 4), WrittenBy: admin}, committed+3))

	require.NoError(t, testStore.WithControlPlane(ctx, func(ctx context.Context) error {
		latest, err := testStore.LatestModuleEnvelopeRecord(ctx)
		require.NoError(t, err)
		require.Equal(t, committed+4, latest.Revision)
		require.Equal(t, admin, latest.WrittenBy)
		require.Empty(t, latest.Signer)
		require.Nil(t, latest.Payload)

		row, err := testStore.LatestDeliveredModuleEnvelopeRecord(ctx)
		require.NoError(t, err)
		require.Equal(t, committed+3, row.Revision)
		require.Empty(t, row.WrittenBy, "a delivered row has no user writer")
		require.Equal(t, "example-release", row.Signer)
		require.Equal(t, "acme", row.OwnershipDomain)
		require.Equal(t, skipped.Payload, row.Payload)
		require.JSONEq(t, string(skipped.Envelope), string(row.Envelope))
		return nil
	}))
}
