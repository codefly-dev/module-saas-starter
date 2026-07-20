package infra

import (
	"context"
	"errors"

	"accounts/pkg/auth"

	scopedpostgres "github.com/codefly-dev/service-postgres/libs/go"
)

type postgresPrincipal struct {
	tenantID string
	userID   string
}

func (principal postgresPrincipal) DatabaseTenantID() string { return principal.tenantID }
func (principal postgresPrincipal) DatabaseUserID() string   { return principal.userID }

// postgresAuthenticator accepts only the private identity installed after JWT
// verification or a trusted gateway assertion. Wool values and transport
// metadata are presentation context, never database authority.
type postgresAuthenticator struct{}

func (postgresAuthenticator) AuthenticatedPrincipal(ctx context.Context) (scopedpostgres.Principal, error) {
	tenantID, userID, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok {
		return nil, scopedpostgres.ErrUnauthenticated
	}
	return postgresPrincipal{tenantID: tenantID, userID: userID}, nil
}

// This first migration slice is read-only. A Writer becomes available only
// after Accounts installs an explicit permission-derived write capability;
// being authenticated alone is intentionally insufficient.
func (postgresAuthenticator) AuthorizeDatabaseWrite(context.Context, scopedpostgres.Principal) error {
	return errors.New("accounts database write capability was not granted")
}

var _ scopedpostgres.Authenticator = postgresAuthenticator{}
