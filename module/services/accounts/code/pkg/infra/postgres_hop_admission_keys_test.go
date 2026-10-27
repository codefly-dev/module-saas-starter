//go:build !pure

package infra_test

import (
	"github.com/codefly-dev/authority/libraries/authority-client/go/stamp"

	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/stretchr/testify/require"
)

func admissionKey(t *testing.T, label string) business.AdmissionKey {
	t.Helper()
	seed := sha256.Sum256([]byte(label))
	public := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	return business.AdmissionKey{ID: stamp.KeyID(public), PublicKey: public}
}

func registerAdmissionKey(store *infra.PostgresStore, key business.AdmissionKey, lease time.Duration, max int) (time.Time, error) {
	expires, _, err := registerAdmissionKeyPublished(store, key, lease, max)
	return expires, err
}

func registerAdmissionKeyPublished(store *infra.PostgresStore, key business.AdmissionKey, lease time.Duration, max int) (time.Time, bool, error) {
	var (
		expires   time.Time
		published bool
	)
	err := store.WithControlPlane(testCtx, func(ctx context.Context) error {
		var err error
		expires, published, err = store.RegisterAdmissionKey(ctx, key, freshRegistration(key.ID), lease, max)
		return err
	})
	return expires, published, err
}

func publishedAdmissionKeys(t *testing.T, store *infra.PostgresStore) []string {
	t.Helper()
	var ids []string
	require.NoError(t, store.WithControlPlane(testCtx, func(ctx context.Context) error {
		keys, err := store.AdmissionKeys(ctx)
		for _, key := range keys {
			ids = append(ids, key.ID)
		}
		return err
	}))
	return ids
}

// secondReplica is another accounts replica's store: its own pools, opened as
// the first one was, over the same database.
func secondReplica(t *testing.T) *infra.PostgresStore {
	t.Helper()
	open := infra.NewPostgresStore
	if raw := os.Getenv("ACCOUNTS_INSTALLER_TEST_DATABASE_URL"); raw != "" {
		open = func(ctx context.Context) (*infra.PostgresStore, error) {
			return infra.NewPostgresStoreFromURL(ctx, raw)
		}
	}
	replica, err := open(testCtx)
	require.NoError(t, err)
	t.Cleanup(replica.Close)
	return replica
}

// with is the published set after a test's own keys join what the shared
// database already publishes.
func with(before []string, ids ...string) []string {
	return append(slices.Clone(before), ids...)
}

// Migration 55 on a real database: a registration is leased by the
// database's clock, renewed in place, overlapped through a rotation, withdrawn
// once it lapses, and bounded; and a second store over the same database (a
// second replica) publishes exactly the same set, because nothing is held in
// a process.
func TestHopAdmissionKeysAreLeasedRowsEveryReplicaPublishes(t *testing.T) {
	replica := secondReplica(t)
	before := publishedAdmissionKeys(t, testStore)

	retiring, next := admissionKey(t, "retiring"), admissionKey(t, "next")
	first, published, err := registerAdmissionKeyPublished(testStore, retiring, business.AdmissionKeyLease, business.MaxAdmissionKeys)
	require.NoError(t, err)
	require.True(t, published, "a first registration is a publication")
	require.WithinDuration(t, time.Now().Add(business.AdmissionKeyLease), first, 30*time.Second)
	require.ElementsMatch(t, with(before, retiring.ID), publishedAdmissionKeys(t, replica), "the other replica does not publish the registration")

	// The hop renews through whichever replica it reaches: one row, renewed.
	renewed, published, err := registerAdmissionKeyPublished(replica, retiring, business.AdmissionKeyLease, business.MaxAdmissionKeys)
	require.NoError(t, err)
	require.False(t, published, "a renewal is not a publication")
	require.False(t, renewed.Before(first))
	require.ElementsMatch(t, with(before, retiring.ID), publishedAdmissionKeys(t, testStore))

	// A rotation overlaps: both keys are published while both are registered.
	_, err = registerAdmissionKey(testStore, next, business.AdmissionKeyLease, business.MaxAdmissionKeys)
	require.NoError(t, err)
	require.ElementsMatch(t, with(before, retiring.ID, next.ID), publishedAdmissionKeys(t, replica))

	// A key no hop renews lapses by the database's clock and is withdrawn.
	brief := admissionKey(t, "brief")
	_, err = registerAdmissionKey(testStore, brief, 50*time.Millisecond, business.MaxAdmissionKeys)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		ids := publishedAdmissionKeys(t, replica)
		return len(ids) == len(before)+2 && !slices.Contains(ids, brief.ID)
	}, 5*time.Second, 20*time.Millisecond, "a lapsed key is still published")

	// The id is derived from the key: the same id for another key never renews.
	forged := business.AdmissionKey{ID: next.ID, PublicKey: admissionKey(t, "forged").PublicKey}
	_, err = registerAdmissionKey(testStore, forged, business.AdmissionKeyLease, business.MaxAdmissionKeys)
	require.ErrorIs(t, err, business.ErrAdmissionKeyMalformed)
}

// The bound holds across replicas: with one slot left, two replicas
// registering different keys at once publish exactly one of them, while a
// registered key still renews at the bound.
func TestTheAdmissionKeyBoundHoldsAcrossReplicas(t *testing.T) {
	replica := secondReplica(t)

	// Two keys of this test's, then one slot left.
	bound := len(publishedAdmissionKeys(t, testStore)) + 3
	held := admissionKey(t, "held")
	for _, key := range []business.AdmissionKey{held, admissionKey(t, "second")} {
		_, err := registerAdmissionKey(testStore, key, business.AdmissionKeyLease, bound)
		require.NoError(t, err)
	}
	// One replica registers the last slot and has not committed yet; the
	// other's registration waits for it and then finds the bound reached,
	// rather than counting without the uncommitted row and registering too.
	racing := admissionKey(t, "racing-b")
	other := make(chan error, 1)
	var (
		early    error
		finished bool
	)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		racer := admissionKey(t, "racing-a")
		if _, _, err := testStore.RegisterAdmissionKey(ctx, racer, freshRegistration(racer.ID), business.AdmissionKeyLease, bound); err != nil {
			return err
		}
		go func() {
			_, err := registerAdmissionKey(replica, racing, business.AdmissionKeyLease, bound)
			other <- err
		}()
		select {
		case early = <-other:
			finished = true
		case <-time.After(300 * time.Millisecond):
		}
		return nil
	}))
	require.False(t, finished, "the other replica did not wait for the last slot to be taken: %v", early)
	require.ErrorIs(t, <-other, business.ErrAdmissionKeysFull)
	require.Len(t, publishedAdmissionKeys(t, testStore), bound)
	_, err := registerAdmissionKey(replica, held, business.AdmissionKeyLease, bound)
	require.NoError(t, err, "a registered key renews at the bound")
}

// freshRegistration is one registration of a key by its own hop instance,
// with a nonce never used.
func freshRegistration(instance string) business.AdmissionRegistration {
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	return business.AdmissionRegistration{Instance: instance, Nonce: hex.EncodeToString(nonce), NonceUntil: time.Now().Add(time.Minute)}
}

// A proof's nonce is used once across replicas, and a hop instance keeps the
// one key it registered while that registration holds.
func TestARegistrationNonceIsUsedOnceAndAnInstanceKeepsOneKeyPostgres(t *testing.T) {
	register := func(key business.AdmissionKey, registration business.AdmissionRegistration) error {
		return testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			_, _, err := testStore.RegisterAdmissionKey(ctx, key, registration, business.AdmissionKeyLease, 1000)
			return err
		})
	}
	first, second := admissionKey(t, "pinned-first"), admissionKey(t, "pinned-second")
	registration := freshRegistration("hop-abcd")
	require.NoError(t, register(first, registration))
	require.ErrorIs(t, register(first, registration), business.ErrAdmissionKeyProof, "a nonce is used once")
	require.NoError(t, register(first, freshRegistration("hop-abcd")), "the same key renews with a fresh proof")
	require.ErrorIs(t, register(second, freshRegistration("hop-abcd")), business.ErrAdmissionKeyPinned, "another key for the instance")
	require.ErrorIs(t, register(first, freshRegistration("hop-efgh")), business.ErrAdmissionKeyPinned, "the key another instance holds")
	require.NoError(t, register(second, freshRegistration("hop-efgh")), "a new key from a new instance")
	require.Subset(t, publishedAdmissionKeys(t, testStore), []string{first.ID, second.ID})
}
