package datasource

import (
	"context"
	"errors"

	"accounts/pkg/business"
)

// SigningSecretStore is the per-source credential store the resolver reads. The
// business Service satisfies it via SourceSigningSecret, a pre-auth
// control-plane lookup keyed by the globally unique source id.
type SigningSecretStore interface {
	SourceSigningSecret(ctx context.Context, sourceID string) (string, error)
}

// StoreSecretResolver resolves a source's webhook signing secret from the
// Vault-transit credential store (issue #274). It is the production replacement
// for StaticSecretResolver's in-memory, single-source map: every source with a
// stored webhook secret becomes verifiable, with no per-source deployment
// config. A source that has no stored secret maps to ErrSourceNotFound so the
// handler answers it exactly like a signature failure, revealing nothing.
type StoreSecretResolver struct {
	store SigningSecretStore
}

// NewStoreSecretResolver wraps the credential store as a SigningSecretResolver.
func NewStoreSecretResolver(store SigningSecretStore) *StoreSecretResolver {
	return &StoreSecretResolver{store: store}
}

func (r *StoreSecretResolver) SigningSecret(ctx context.Context, sourceID string) (string, error) {
	secret, err := r.store.SourceSigningSecret(ctx, sourceID)
	if errors.Is(err, business.ErrSourceCredentialNotFound) {
		return "", ErrSourceNotFound
	}
	if err != nil {
		return "", err
	}
	return secret, nil
}
