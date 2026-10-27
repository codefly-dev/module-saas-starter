package pgauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/auth"
)

// LookupIdentity projects an existing active person's identity for discovery.
// It uses login's organization selection, but never provisions, bootstraps an
// administrator, touches login recency or allocates a session. Only person,
// organization, organization role and display fields are populated; this is
// not an authenticated identity to pass to a token minter.
func (r *Resolver) LookupIdentity(ctx context.Context, claims *auth.Claims) (*auth.Identity, error) {
	if err := claims.Valid(); err != nil {
		return nil, err
	}
	if r.bootstrap == nil {
		return nil, errors.New("pgauth: bootstrap store is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var identity *auth.Identity
	err := r.bootstrap.WithAuthLookupTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		userID, found, err := r.findIdentity(ctx, tx, claims)
		if err != nil {
			return err
		}
		if !found {
			return auth.ErrNoAccount
		}
		orgID, role, err := r.selectOrg(ctx, tx, userID, claims.ProviderOrgID)
		if err != nil {
			return err
		}
		identity = &auth.Identity{
			UserID: userID, OrgID: orgID, OrgRole: role,
			Email: strings.ToLower(claims.Email), DisplayName: strings.TrimSpace(claims.DisplayName),
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pgauth: lookup identity: %w", err)
	}
	return identity, nil
}
