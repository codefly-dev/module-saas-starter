package infra

import (
	"context"

	scopedpostgres "github.com/codefly-dev/service-postgres/libs/go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityScopeProbe exposes the scope query ListAdministeredOrganizations runs,
// so a test can assert what admits a control-plane transaction. Deciding on the
// role attribute rather than the assumed role is invisible on a profile that
// grants BYPASSRLS and fatal on the managed one, which grants none.
const IdentityScopeProbe = identityScopeProbe

// ControlPlaneDatabaseRole is the role withControlPlaneTx assumes.
const ControlPlaneDatabaseRole = controlPlaneDatabaseRole

// LoginAuthority is what a connection's login can reach, as startup reads it.
type LoginAuthority = loginAuthority

// InspectLoginAuthority reads the authority every new request connection judges
// its login by, so the suite pins exactly what requireRequestLoginAuthority
// refuses.
func InspectLoginAuthority(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (LoginAuthority, error) {
	return inspectLoginAuthority(ctx, q, requestLoginBoundary)
}

// BeyondTenant lists what authority reaches past the tenant boundary.
func BeyondTenant(authority LoginAuthority) []string { return authority.beyondTenant() }

// ControlPlanePool exposes the pool WithControlPlane runs on, so a test can see
// what a released control-plane connection comes back as.
func ControlPlanePool(s *PostgresStore) *pgxpool.Pool { return s.controlPlane }

// StoreOnPool wraps pool as a store that installs no connection hook of its
// own, so a test can see what WithOrgTx, WithUserTx and As(identity) read from a
// connection that no release hook has reset.
func StoreOnPool(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{Close: pool.Close, pool: pool, controlPlane: pool}
}

// UseMemberPermissions replaces the generated catalog's member grant for one
// test, so the SQL branch is exercised without a composed catalog.
func UseMemberPermissions(held func(resource, action string) bool) (restore func()) {
	previous := isMemberPermission
	isMemberPermission = held
	return func() { isMemberPermission = previous }
}

// InspectReaderAuthority reads the authority the scoped reader's capability is
// judged by, so the suite pins exactly what requireReaderLoginAuthority refuses
// and can assert that a correctly provisioned reader holds nothing beyond it.
func InspectReaderAuthority(ctx context.Context, conn interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}) (LoginAuthority, error) {
	return inspectReaderAuthority(ctx, conn)
}

// BeyondReadOnly lists what authority reaches past a read-only capability.
func BeyondReadOnly(authority LoginAuthority) []string { return authority.beyondReadOnly() }

// RequireReaderLoginAuthority is the judgement verifyReaderLogin applies to the
// read-only capability at startup.
func RequireReaderLoginAuthority(ctx context.Context, conn interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}) error {
	return requireReaderLoginAuthority(ctx, conn)
}

// OpenScopedBoundary exposes the scoped-pool constructor so the suite can
// exercise the reader and writer capability policies the boundary installs,
// independently of the request pool NewPostgresStoreWithCapabilities opens
// beside it. Open pings both scoped pools, so a capability whose login is
// widened is refused here rather than on first use.
func OpenScopedBoundary(ctx context.Context, readOnlyConnection, readWriteConnection string) (func(), error) {
	_, closeDatabase, err := openScopedBoundary(ctx, readOnlyConnection, readWriteConnection, nil)
	return closeDatabase, err
}

// writeGrantingAuthenticator is the production authenticator with exactly one
// decision substituted: it permits a database write. Production refuses
// AuthorizeDatabaseWrite outright, so Factory.Writer returns before it reaches
// the pool and the writer capability's connection policy is never exercised by
// a checkout — which is precisely the boundary a startup-only validation would
// also pass. This type is declared in a _test file, so it is never compiled
// into the binary and the production refusal is untouched.
//
// Everything else is the production path: the same openScopedBoundaryAs, the
// same transport validation, the same scope settings, operation timeout,
// restricted-session policy and connection policies.
type writeGrantingAuthenticator struct{ postgresAuthenticator }

func (writeGrantingAuthenticator) AuthorizeDatabaseWrite(context.Context, scopedpostgres.Principal) error {
	return nil
}

// OpenScopedBoundaryWithWrites opens the production boundary with the writer
// capability reachable, and returns the Factory so the suite can borrow the
// library-owned writer pool repeatedly on ONE retained boundary. That is what
// makes a checkout judgement distinguishable from a connect-only one: the
// boundary stays open across a widening, so a refusal afterwards cannot have
// come from a fresh startup validation.
//
// It returns the Factory, not a pool: the pools stay private to the primitive.
func OpenScopedBoundaryWithWrites(ctx context.Context, readOnlyConnection, readWriteConnection string) (*scopedpostgres.Factory, func(), error) {
	return openScopedBoundaryAs(ctx, readOnlyConnection, readWriteConnection, nil, writeGrantingAuthenticator{})
}
