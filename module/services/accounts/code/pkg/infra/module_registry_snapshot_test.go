//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/internal/deliverytest"
	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Pause after the actual SELECT has materialized its rows. The callback commits
// through another connection; it never changes or substitutes the read result.
type admissionBetweenRegistryReadsStore struct {
	*infra.PostgresStore
	afterRead func()
}

func (s *admissionBetweenRegistryReadsStore) ListSolutionHostBindings(ctx context.Context) ([]*business.SolutionHostBindingRecord, error) {
	records, err := s.PostgresStore.ListSolutionHostBindings(ctx)
	if err == nil && s.afterRead != nil {
		after := s.afterRead
		s.afterRead = nil
		after()
	}
	return records, err
}

func TestRegistrySnapshotCannotRestoreAuthorityDuringFirstAdmission(t *testing.T) {
	s := newRegistryScenario(t, "snapshot-admission")
	revision := s.putEnvelope(t, envelopeOptions{resources: true})
	s.deliverPresence(t, s.primary, 1, revision)
	s.deliverAuthority(t, s.authority(t, s.primary, 1, revision, true, nil))
	renamed := s.authority(t, s.primary, 1, revision, false, s.grantsTo(s.primary))
	renamed.Authority += "-renamed"
	s.deliverAuthority(t, renamed)

	store := &admissionBetweenRegistryReadsStore{PostgresStore: testStore}
	replica, err := business.NewService(store)
	require.NoError(t, err)
	replica.SetModuleEnvelopeStore(testStore)
	replica.SetModuleAuthorityReads(testStore, nil)
	verifier := productionVerifier(t)
	policy := map[string][]string{productionPathSigner: {productionPathDomain}}
	replica.SetSolutionDelivery(verifier, testStore, nil, policy)
	presenceReconciler := newRegistryReconciler(t, replica, verifier, policy, replica)
	registry, err := business.NewModulePrincipalReconciler(presenceReconciler.AuthorityActivation())
	require.NoError(t, err)

	// The first replica has delivered presence, but neither replica has applied
	// it yet. Run just the registry on the second replica.
	require.ErrorIs(t, registry.RunOnce(testCtx), business.ErrModulePrincipalOmitted)
	_, present := entryOn(replica, s.prefix)
	require.False(t, present)

	committed := false
	store.afterRead = func() {
		// Real core admission and Postgres commit while the other replica's
		// snapshot remains open. Its reads hold no write lock on this binding.
		require.NotErrorIs(t, s.passReasons(t), business.ErrModulePrincipalInputsUnreadable)
		records, err := s.delivery.service.ListSolutionHostBindings(testCtx)
		require.NoError(t, err)
		for _, record := range records {
			if record.BindingID == s.primary.binding {
				require.NotNil(t, record.Applied)
				require.Equal(t, productionPathDomain, record.Applied.Domain)
				committed = true
			}
		}
		require.True(t, committed, "the first admission must actually commit")
	}
	err = registry.RunOnce(testCtx)
	require.True(t, committed)
	_, present = entryOn(replica, s.prefix)
	require.False(t, present, "new presence must not be paired with an ownerless withdrawal map")
	require.ErrorIs(t, err, business.ErrModulePrincipalOmitted)
	grant, err := replica.AuthorizeModuleCapability(testCtx, s.caller(s.tenant))
	require.Error(t, err)
	require.Nil(t, grant)

	require.ErrorIs(t, registry.RunOnce(testCtx), business.ErrSolutionAuthorityWithdrawn)
	_, present = entryOn(replica, s.prefix)
	require.False(t, present, "the next snapshot sees the admitted owner and its terminal tombstone together")
}

func TestControlPlaneReadSnapshotKeepsPresenceAndWithdrawalHistoryTogether(t *testing.T) {
	alias := uniqueAlias("registry-snapshot")
	binding := "binding-" + alias
	now := time.Now().UTC()
	applied := appliedPresenceRecord(t, binding, alias, 1, now, false)
	raw, err := solutionhost.FixtureDocument(solutionhost.DocumentTypeAuthority, "valid")
	require.NoError(t, err)
	document, err := solutionhost.ParseAuthority(raw)
	require.NoError(t, err)
	document.Authority, document.PresenceBinding = "authority-"+alias, binding
	document.Removed = true
	document.ApprovedBuild, document.EffectiveFrom, document.Principals = "", 0, nil
	document.Queues, document.Namespaces, document.ScopeCeilings = []string{}, []string{}, []solutionhost.ScopeCeiling{}
	document.OwnershipDomain = applied.Applied.Domain
	document.Host = solutionhost.HostTarget{Coordinate: applied.HostCoordinate, Component: applied.HostComponent}
	payload, err := document.CanonicalBytes()
	require.NoError(t, err)
	withdrawal := &business.SolutionDeliveryRecord{
		Kind: business.SolutionDeliveryAuthority, DocumentID: document.Authority,
		Generation: document.Generation, ContentHash: fixtureDigest(string(payload)),
		Payload: payload, Bundle: deliverytest.Envelope(t, payload, solutionhost.FixtureDeliveredBy),
		SignerIdentity: productionPathSigner, OwnershipDomain: document.OwnershipDomain, ReceivedAt: now,
	}
	assertVisible := func(ctx context.Context, want bool) {
		t.Helper()
		records, err := testStore.ListSolutionHostBindings(ctx)
		require.NoError(t, err)
		found := false
		for _, record := range records {
			found = found || record.BindingID == binding
		}
		require.Equal(t, want, found, "applied presence")
		for _, read := range []func(context.Context, business.SolutionDeliveryKind) ([]*business.SolutionDeliveryRecord, error){
			testStore.ListDeliveredDocuments, testStore.ListNewestDeliveredDocuments,
		} {
			rows, err := read(ctx, business.SolutionDeliveryAuthority)
			require.NoError(t, err)
			found = false
			for _, row := range rows {
				found = found || row.DocumentID == document.Authority
			}
			require.Equal(t, want, found, "withdrawal history and newest authority")
		}
	}
	require.NoError(t, testStore.WithControlPlaneReadSnapshot(testCtx, func(ctx context.Context) error {
		assertVisible(ctx, false)
		// Deliberately use the outer context: a different transaction commits
		// both relations after the snapshot's first read.
		require.NoError(t, testStore.WithControlPlane(testCtx, func(writer context.Context) error {
			if err := testStore.SaveSolutionHostBinding(writer, applied); err != nil {
				return err
			}
			_, err := testStore.RecordDeliveredDocument(writer, withdrawal)
			return err
		}))
		assertVisible(ctx, false)
		return nil
	}))
	require.NoError(t, testStore.WithControlPlaneReadSnapshot(testCtx, func(ctx context.Context) error {
		assertVisible(ctx, true)
		return nil
	}))

	// READ ONLY must be enforced by Postgres, even for the control-plane role.
	err = testStore.WithControlPlaneReadSnapshot(testCtx, func(ctx context.Context) error {
		return testStore.SaveSolutionHostBinding(ctx, applied)
	})
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "25006", pgErr.Code)
}
