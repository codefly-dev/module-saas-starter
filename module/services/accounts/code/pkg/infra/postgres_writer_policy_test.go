//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/internal/testdb"
	"accounts/pkg/infra"
)

// service-postgres's Open owns TWO pools, and the scoped writer is a physically
// separate pool from the request pool accounts opens beside it. It
// authenticates as the request login, but requireRequestLoginAuthority — wired
// to requestPool's own AfterConnect — never saw it. Accounts calls only
// Reader(), and its authenticator refuses AuthorizeDatabaseWrite outright, so
// the writer carried no live traffic; an unjudged connection on the request
// credential is a surface whether or not anything currently borrows it.
//
// writerConnectionPolicy closes that, and this is what proves it. Open pings
// both scoped pools, so a widened writer login is refused as the boundary
// opens. Driving openScopedBoundary directly is what isolates the capability:
// NewPostgresStoreWithCapabilities judges the same login on its own request
// pool first and would fail with that pool's message instead, which would
// prove requireRequestLoginAuthority works but say nothing about whether the
// writer capability received it.

// widenWriter applies and undoes authority on a login through the store owner.
func widenWriter(t *testing.T, statement string) {
	t.Helper()
	owner, err := pgx.Connect(testCtx, storeSecret(t, "owner-connection"))
	require.NoError(t, err)
	defer owner.Close(context.Background()) //nolint:errcheck // closing a test fixture connection
	_, err = owner.Exec(testCtx, statement)
	require.NoError(t, err, statement)
}

func TestScopedWriterCapabilityIsJudgedLikeTheRequestLogin(t *testing.T) {
	// A request-shaped login: app_tenant and nothing else, as the agent
	// provisions the read-write capability.
	writer, err := testdb.CreateLogin(testCtx, "scoped_writer_policy", "app_tenant")
	require.NoError(t, err)
	reader := readerConnection(t)

	// The valid profile opens: the policy admits a correctly provisioned
	// writer, so a refusal below is the widening and not the policy refusing
	// everything.
	closeBoundary, err := infra.OpenScopedBoundary(testCtx, reader, writer)
	require.NoError(t, err, "a correctly provisioned writer must open the scoped boundary")
	closeBoundary()

	for _, widening := range []struct {
		name, widen, restore, want string
	}{
		{
			// Relation ownership: an owner's own tables do not apply row-level
			// security to it unless forced, and the owner may drop FORCE. The
			// library's restricted-session policy judges DATABASE ownership,
			// not this, so only the application policy can refuse it.
			"owns a relation",
			"CREATE TABLE scoped_writer_owned(id int); ALTER TABLE scoped_writer_owned OWNER TO scoped_writer_policy",
			"DROP TABLE IF EXISTS scoped_writer_owned",
			"owns a relation",
		},
		{
			// A privilege app_tenant does not hold, granted to the login
			// itself, survives SET ROLE NONE.
			"granted a privilege app_tenant does not hold",
			"GRANT CREATE ON SCHEMA public TO scoped_writer_policy",
			"REVOKE CREATE ON SCHEMA public FROM scoped_writer_policy",
			"CREATE",
		},
	} {
		t.Run(widening.name, func(t *testing.T) {
			widenWriter(t, widening.widen)
			restored := false
			t.Cleanup(func() {
				if !restored {
					widenWriter(t, widening.restore)
				}
			})

			_, err := infra.OpenScopedBoundary(testCtx, reader, writer)
			require.Error(t, err, "the scoped writer must be refused when its login reaches beyond app_tenant")
			require.ErrorContains(t, err, "connection policy",
				"the refusal must come from the capability's connection policy")
			require.ErrorContains(t, err, widening.want)

			widenWriter(t, widening.restore)
			restored = true
			closeBoundary, err := infra.OpenScopedBoundary(testCtx, reader, writer)
			require.NoError(t, err, "the writer opens again once the widening is undone")
			closeBoundary()
		})
	}
}
