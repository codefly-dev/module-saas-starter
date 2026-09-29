//go:build !pure

package infra_test

import (
	"context"
	"testing"

	scopedpostgres "github.com/codefly-dev/service-postgres/libs/go"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/internal/testdb"
	"accounts/pkg/auth"
	"accounts/pkg/infra"
)

// service-postgres's Open owns TWO pools, and the scoped writer is a physically
// separate pool from the request pool accounts opens beside it. It
// authenticates as the request login, but requireRequestLoginAuthority — wired
// to requestPool's own AfterConnect — never saw it. Accounts calls only
// Reader(), and its authenticator refuses AuthorizeDatabaseWrite outright, so
// the writer carries no live traffic; an unjudged connection on the request
// credential is a surface whether or not anything currently borrows it.
//
// What has to be proved is a CHECKOUT judgement, which means ONE retained
// boundary across the widening. Closing a boundary and opening a fresh one
// proves only the connect path, and a startup-only validation would pass that
// just as well. So these keep the boundary open, establish by backend identity
// that a borrow is reusing the connection an earlier borrow left in the pool,
// widen the login while it sits idle, and require the next borrow to be
// refused before the application callback runs.

// backendOf borrows the writer and returns the backend PID serving it, plus
// whether the application callback ran at all.
func writerBackend(t *testing.T, ctx context.Context, factory *scopedpostgres.Factory) (int, error) {
	t.Helper()
	writer, err := factory.Writer(ctx)
	if err != nil {
		return 0, err
	}
	var pid int
	err = writer.InTransaction(ctx, func(ctx context.Context, tx scopedpostgres.WriteTx) error {
		// A read inside a write transaction: this takes the writer pool's
		// checkout without mutating the store.
		return tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid)
	})
	return pid, err
}

// widenLogin applies or undoes authority through the store owner.
func widenLogin(t *testing.T, statement string) {
	t.Helper()
	owner, err := pgx.Connect(testCtx, storeSecret(t, "owner-connection"))
	require.NoError(t, err)
	defer owner.Close(context.Background()) //nolint:errcheck // closing a test fixture connection
	_, err = owner.Exec(testCtx, statement)
	require.NoError(t, err, statement)
}

// backendIsLive reports whether a backend PID is still a live session, so
// "the same PID twice" is evidence of reuse rather than of a recycled number.
func backendIsLive(t *testing.T, pid int) bool {
	t.Helper()
	var live bool
	require.NoError(t, testPool.QueryRow(testCtx,
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity WHERE pid = $1)`, pid).Scan(&live))
	return live
}

// The writer capability's policy, judged on a RETAINED boundary at checkout.
// Each widening is a class the library's restricted-session policy does not
// judge, so what refuses the borrow can only be this module's own policy.
func TestScopedWriterPolicyRefusesAWidenedBackendAtTheNextCheckout(t *testing.T) {
	writerDSN, err := testdb.CreateLogin(testCtx, "scoped_writer_policy", "app_tenant")
	require.NoError(t, err)
	readerDSN := readerConnection(t)

	factory, closeBoundary, err := infra.OpenScopedBoundaryWithWrites(testCtx, readerDSN, writerDSN)
	require.NoError(t, err, "the correctly provisioned writer must open the boundary")
	t.Cleanup(closeBoundary)

	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	ctx := auth.WithVerifiedDatabaseIdentity(testCtx, userID, orgID)

	// Valid-profile acceptance, and proof that the SECOND borrow reuses the
	// backend the first one left in the pool rather than reconnecting. Without
	// this, a refusal later could be a fresh connection being judged at connect.
	first, err := writerBackend(t, ctx, factory)
	require.NoError(t, err, "the valid profile must serve a write transaction")
	second, err := writerBackend(t, ctx, factory)
	require.NoError(t, err)
	require.Equal(t, first, second, "the second borrow must reuse the same backend; otherwise this proves connect, not checkout")
	require.True(t, backendIsLive(t, second), "that backend must still be a live session, so the identity means reuse")
	retained := second

	for _, widening := range []struct {
		name, widen, restore, want string
	}{
		{
			// Relation ownership: an owner's tables do not apply row-level
			// security to it unless forced, and the owner may drop FORCE. The
			// library judges DATABASE ownership, not this.
			"owns a relation",
			"CREATE TABLE scoped_writer_owned(id int); ALTER TABLE scoped_writer_owned OWNER TO scoped_writer_policy",
			"DROP TABLE IF EXISTS scoped_writer_owned",
			"owns a relation",
		},
		{
			// A privilege app_tenant does not hold, granted to the login
			// itself, survives SET ROLE NONE.
			"granted a privilege beyond app_tenant",
			"GRANT CREATE ON SCHEMA public TO scoped_writer_policy",
			"REVOKE CREATE ON SCHEMA public FROM scoped_writer_policy",
			"CREATE",
		},
		{
			// A SECURITY DEFINER function runs as its owner, so executing one
			// lends the writer that owner's authority.
			"can execute a SECURITY DEFINER function beyond app_tenant",
			`CREATE FUNCTION scoped_writer_definer() RETURNS bigint LANGUAGE sql SECURITY DEFINER AS 'SELECT 1';
			 REVOKE ALL ON FUNCTION scoped_writer_definer() FROM PUBLIC;
			 GRANT EXECUTE ON FUNCTION scoped_writer_definer() TO scoped_writer_policy`,
			"DROP FUNCTION IF EXISTS scoped_writer_definer()",
			"EXECUTE",
		},
	} {
		t.Run(widening.name, func(t *testing.T) {
			require.True(t, backendIsLive(t, retained),
				"the boundary is still open and its backend still pooled before the widening")
			widenLogin(t, widening.widen)
			restored := false
			t.Cleanup(func() {
				if !restored {
					widenLogin(t, widening.restore)
				}
			})

			// The boundary was never closed and never reopened, so a refusal
			// here is the checkout judgement on a pooled backend.
			callbackRan := false
			writer, err := factory.Writer(ctx)
			require.NoError(t, err, "authorization is unchanged; the refusal must come from the connection, not the authorizer")
			err = writer.InTransaction(ctx, func(context.Context, scopedpostgres.WriteTx) error {
				callbackRan = true
				return nil
			})
			require.Error(t, err, "a widened writer login must be refused at the next checkout")
			require.False(t, callbackRan, "the refusal must land before the application callback runs")
			require.ErrorContains(t, err, "connection policy",
				"the refusal must carry the capability's connection-policy provenance")
			require.ErrorContains(t, err, "read-write", "and name the read-write capability")
			require.ErrorContains(t, err, "Postgres login", "and this module's own login judgement")
			require.ErrorContains(t, err, widening.want)

			// Recovery on the same retained boundary.
			widenLogin(t, widening.restore)
			restored = true
			recovered, err := writerBackend(t, ctx, factory)
			require.NoError(t, err, "the writer serves again once the widening is undone, without reopening the boundary")
			require.NotZero(t, recovered)
			retained = recovered
		})
	}

	final, err := writerBackend(t, ctx, factory)
	require.NoError(t, err, "the valid profile still serves at the end")
	require.NotZero(t, final)
}
