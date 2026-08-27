package datasource

import (
	"context"
	"errors"
	"strings"
)

// StaticSecretResolver resolves per-source signing secrets from an in-memory
// map. It is the receipt-time seam the per-source credential store (issue #274,
// Vault transit) replaces; until then a single GitHub source is configured from
// the environment.
type StaticSecretResolver struct {
	secrets map[string]string
}

// NewStaticSecretResolver builds a resolver over a copy of secrets. An entry
// with an empty id or secret fails at construction rather than silently
// admitting an unsigned delivery at request time.
func NewStaticSecretResolver(secrets map[string]string) (*StaticSecretResolver, error) {
	copied := make(map[string]string, len(secrets))
	for id, secret := range secrets {
		id = strings.TrimSpace(id)
		if id == "" || strings.TrimSpace(secret) == "" {
			return nil, errors.New("datasource: source id and signing secret are required")
		}
		copied[id] = secret
	}
	return &StaticSecretResolver{secrets: copied}, nil
}

func (r *StaticSecretResolver) SigningSecret(_ context.Context, sourceID string) (string, error) {
	secret, ok := r.secrets[sourceID]
	if !ok {
		return "", ErrSourceNotFound
	}
	return secret, nil
}
