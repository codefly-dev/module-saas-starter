package main

import (
	"context"
	"errors"

	"accounts/pkg/business"
	"accounts/pkg/datasource"
)

// datasourceSigningSecretResolver adapts the Vault-transit credential store
// (issue #274) to the inbound-webhook receiver's resolver seam (issue #275). It
// maps the store's not-found sentinel to the receiver's so an unknown or
// webhook-unconfigured source stays a quiet 404 rather than a logged error, and
// keeps the receiver decoupled from the business package.
type datasourceSigningSecretResolver struct{ svc *business.Service }

func (r datasourceSigningSecretResolver) SigningSecret(ctx context.Context, sourceID string) (string, error) {
	secret, err := r.svc.SigningSecret(ctx, sourceID)
	if errors.Is(err, business.ErrDatasourceSourceNotFound) {
		return "", datasource.ErrSourceNotFound
	}
	return secret, err
}
