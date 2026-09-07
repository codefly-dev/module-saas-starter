package main

import (
	"context"
	"errors"

	"accounts/pkg/business"
	"accounts/pkg/datasource"
)

// datasourceSourceResolver adapts the Vault-transit credential store (issue
// #274) to the inbound-webhook receiver's resolver seam. It resolves the signing
// secret and the tenant/boundary attribution the change-set compiler needs
// (issue #487) in one control-plane read, maps the store's not-found sentinel to
// the receiver's so an unknown or webhook-unconfigured source stays a quiet 404,
// and keeps the receiver decoupled from the business package.
type datasourceSourceResolver struct{ svc *business.Service }

func (r datasourceSourceResolver) ResolveSource(ctx context.Context, sourceID string) (datasource.ResolvedSource, error) {
	source, err := r.svc.ResolveWebhookSource(ctx, sourceID)
	if errors.Is(err, business.ErrDatasourceSourceNotFound) {
		return datasource.ResolvedSource{}, datasource.ErrSourceNotFound
	}
	if err != nil {
		return datasource.ResolvedSource{}, err
	}
	return datasource.ResolvedSource{
		SigningSecret: source.SigningSecret,
		OrgID:         source.OrgID,
		BoundaryID:    source.BoundaryID,
	}, nil
}
