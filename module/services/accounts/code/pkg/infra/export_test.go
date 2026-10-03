package infra

import (
	"context"

	"accounts/pkg/infra/internal/txbind"

	scopedpostgres "github.com/codefly-dev/service-postgres/libs/go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// VerifySignedEntityInput is verify.SignedEntity under a local name, exported so
// an external test can embed it when building a signed entity with one field
// changed — which is how the transparency-evidence refusal is exercised without
// hand-assembling a bundle.
type VerifySignedEntityInput = verify.SignedEntity

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

// BindControlPlaneTx binds tx to ctx as a control-plane transaction, exactly as
// WithControlPlane binds its own, so a test can hand a store method a
// transaction it opened itself.
//
// The binding key is internal to pkg/infra by design: only the code that opens
// a transaction may say what authority it carries, so nothing outside can forge
// one. Before that, a caller could inject a transaction with a plain string
// context key; a test still doing so is not refused, it is silently ignored,
// and the store method falls back to the request pool — where a control-plane
// operation fails on a permission it would never have lacked. This is the
// supported way to do it.
func BindControlPlaneTx(ctx context.Context, tx pgx.Tx) context.Context {
	return txbind.BindControlPlane(ctx, tx)
}

// ExecAsControlPlane runs one statement inside a real control-plane
// transaction, for test states no tenant write path produces directly (an
// expiry that has already passed, a role demoted behind the API's back).
//
// It exists because the obvious shortcut is silently wrong. A test outside this
// package cannot reach the transaction WithControlPlane opens — the binding key
// is internal by design, so that only the code opening a transaction may say
// what authority it carries. A test that pulled the transaction out of the
// context with a plain string key (`ctx.Value("tx").(pgx.Tx)`) worked only for
// as long as that was the binding, and when the binding moved it did not start
// failing on a permission: it read nil and panicked, which is the better of the
// two outcomes. The other, described on BindControlPlaneTx above, is a silent
// fall back to the request pool.
//
// Routing through WithControlPlane keeps the role handling in exactly one
// place, so a test cannot run as the control plane in a way production never
// does.
func (s *PostgresStore) ExecAsControlPlane(ctx context.Context, sql string, args ...any) error {
	return s.WithControlPlane(ctx, func(ctx context.Context) error {
		_, err := s.getQueryExecutor(ctx).Exec(ctx, sql, args...)
		return err
	})
}

// VerifySignedEntity exposes the keyless verifier's DECISION, so a test can
// drive it with an in-process Sigstore instead of a hand-assembled bundle
// document.
//
// Exported for tests deliberately rather than testing through VerifyBundle's
// JSON: the thing worth proving is that the policy accepts and refuses the right
// identities against a real trust root, and a bundle written by hand proves only
// that the decoder works. The path under test is the one production takes — the
// JSON entry point decodes and calls straight into this.
func VerifySignedEntity(
	verifier any, entity VerifySignedEntityInput, payload []byte,
) (string, error) {
	return verifier.(*keylessBundleVerifier).verifySignedEntity(entity, payload)
}

// NewKeylessVerifierWithoutPolicyValidation builds the keyless verifier over
// trust material and a policy WITHOUT running the policy's boot validation.
//
// It exists for one reason, and the reason is a limitation worth stating rather
// than hiding. Production policy hygiene requires every signer to name a source
// repository or a build config, because a workflow identity alone matches that
// same workflow path in every fork of a repository. The in-process Sigstore used
// in tests mints leaf certificates carrying ONLY the OIDC issuer extension — it
// has no way to emit `SourceRepositoryURI` — so no certificate it can produce
// satisfies a policy that production would accept.
//
// So the accept path is exercised with a policy shape production REFUSES, and
// the refusal of that shape is asserted separately by the Validate tests. What
// is genuinely not covered end to end is the extension matching itself: that
// `SourceRepositoryURI` and `SourceRepositoryRef` are compared at all is
// delegated to sigstore-go and exercised only by its own suite.
func NewKeylessVerifierWithoutPolicyValidation(
	trustedMaterial trustedMaterialForTest, policy *SolutionHostVerificationPolicy,
) (any, error) {
	verifier, err := verify.NewVerifier(trustedMaterial,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return nil, err
	}
	return &keylessBundleVerifier{verifier: verifier, policy: policy}, nil
}

// trustedMaterialForTest is root.TrustedMaterial under a local name.
type trustedMaterialForTest = root.TrustedMaterial

// KeylessSignerDomains reads the signer-to-domain mapping off a verifier built
// by either constructor, so a test can assert the mapping travels with the
// policy it came from.
func KeylessSignerDomains(verifier any) map[string][]string {
	return verifier.(*keylessBundleVerifier).SignerDomains()
}
