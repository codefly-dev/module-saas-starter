package datasource_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/datasource"
)

type fakeSigningStore struct {
	secret string
	err    error
	calls  []string
}

func (f *fakeSigningStore) SourceSigningSecret(_ context.Context, sourceID string) (string, error) {
	f.calls = append(f.calls, sourceID)
	return f.secret, f.err
}

func TestStoreSecretResolver_ReturnsStoredSecret(t *testing.T) {
	store := &fakeSigningStore{secret: "whsec-live"}
	resolver := datasource.NewStoreSecretResolver(store)

	secret, err := resolver.SigningSecret(context.Background(), "src-1")
	require.NoError(t, err)
	require.Equal(t, "whsec-live", secret)
	require.Equal(t, []string{"src-1"}, store.calls)
}

func TestStoreSecretResolver_MapsNotFoundSentinel(t *testing.T) {
	store := &fakeSigningStore{err: business.ErrSourceCredentialNotFound}
	resolver := datasource.NewStoreSecretResolver(store)

	_, err := resolver.SigningSecret(context.Background(), "unknown")
	require.ErrorIs(t, err, datasource.ErrSourceNotFound)
}

func TestStoreSecretResolver_PropagatesOtherErrors(t *testing.T) {
	boom := errors.New("vault unreachable")
	store := &fakeSigningStore{err: boom}
	resolver := datasource.NewStoreSecretResolver(store)

	_, err := resolver.SigningSecret(context.Background(), "src-1")
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, datasource.ErrSourceNotFound)
}
