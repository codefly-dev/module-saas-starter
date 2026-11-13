//go:build !pure

package infra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
)

// The inbox holds one policy pair per binding and generation, whoever asks: the
// same carrier and authority document again is a replay that writes nothing;
// a different authority document, or none, at a generation it holds is a
// rewrite and is refused as a conflict naming both digests. The authority core
// refuses such a delivery before it reaches the inbox on the composition path
// (TestCompositionPersistsOneImmutablePolicyPair), so this holds the inbox's
// own check where nothing in front of it does.
func TestTheInboxRefusesAnotherAuthorityDocumentAtAGenerationItHolds(t *testing.T) {
	m := productionPathManifest(t, uniqueAlias("inbox-pair"), 1)
	payload, err := solutionhost.SignedPayload(m)
	require.NoError(t, err)
	digest, err := m.Digest()
	require.NoError(t, err)
	record := func(authority []byte) *business.SolutionDeliveryRecord {
		return &business.SolutionDeliveryRecord{Binding: m.Binding, Generation: m.Generation, ContentHash: digest, Payload: payload,
			Envelope: deliverytest.Envelope(t, payload, productionPathSigner), SignerIdentity: productionPathSigner, OwnershipDomain: productionPathDomain,
			ReceivedAt: time.Now().UTC(), AuthorityDocument: authority, AuthorityEnvelope: authority}
	}
	// Rolled back: the rows are this test's, never another test's inbox.
	done := errors.New("inbox conflict checked")
	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		inserted, err := testStore.RecordDeliveredDocument(ctx, record([]byte("read")))
		require.NoError(t, err)
		require.True(t, inserted)

		inserted, err = testStore.RecordDeliveredDocument(ctx, record([]byte("read")))
		require.NoError(t, err, "an exact replay")
		require.False(t, inserted, "a replay writes nothing")

		_, err = testStore.RecordDeliveredDocument(ctx, record([]byte("write")))
		require.ErrorIs(t, err, business.ErrSolutionDeliveryConflict)
		require.ErrorContains(t, err, "authority document digest stored=sha256:")
		require.ErrorContains(t, err, "(present=true) offered=sha256:")

		_, err = testStore.RecordDeliveredDocument(ctx, record(nil))
		require.ErrorIs(t, err, business.ErrSolutionDeliveryConflict, "a replay cannot strip the policy")
		require.ErrorContains(t, err, "(present=false)")
		return done
	})
	require.ErrorIs(t, err, done)
}
