//go:build integration

package adapters

import (
	"context"
	"os"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/infra"
	"connectrpc.com/connect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Requires an independently migrated database. Never resolves a shared stack.
func TestSourceReadPostgresSignedRPC(t *testing.T) {
	conn := os.Getenv("SOURCE_READ_TEST_CONN")
	if conn == "" {
		t.Fatal("SOURCE_READ_TEST_CONN must name an independent migrated database")
	}
	_, facts, client, mint := sourceReadFixture(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, conn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	boundary, role, source := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) { _, err := pool.Exec(ctx, sql, args...); require.NoError(t, err) }
	exec(`INSERT INTO users(uuid,primary_email) VALUES($1,'reader@example.com')`, readOwner)
	exec(`INSERT INTO organizations(id,name,slug,owner_id) VALUES($1,'Example Organization',$2,$3)`, readOrg, "read-"+uuid.NewString(), readOwner)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, readOrg)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE uuid=$1`, readOwner)
		_, _ = pool.Exec(ctx, `DELETE FROM principals WHERE id=$1`, readOwner)
	})
	exec(`INSERT INTO principals(id,kind,display_name) VALUES($1,'human','Example Reader') ON CONFLICT(id) DO NOTHING`, readOwner)
	exec(`INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'member')`, readOrg, readOwner)
	exec(`INSERT INTO roles(id,name,org_id) VALUES($1,'reader',$2)`, role, readOrg)
	exec(`INSERT INTO role_permissions(role_id,resource,action) VALUES($1,'documents','read')`, role)
	exec(`INSERT INTO role_assignments(org_id,subject_id,subject_kind,role_id) VALUES($1,$2,'principal',$3)`, readOrg, readOwner, role)
	exec(`INSERT INTO scope_nodes(id,org_id,scope_path,kind,label) VALUES($1,$2,'root.collection','collection','Example Collection')`, boundary, readOrg)
	exec(`INSERT INTO scope_grants(org_id,subject_id,subject_kind,scope_path,role_id) VALUES($1,$2,'principal','root.collection',$3)`, readOrg, readOwner, role)
	exec(`INSERT INTO datasource_sources(id,org_id,provider,repo,paths,branch,credential_secret_ref,boundary_node_id) VALUES($1,$2,'github','acme/handbook',ARRAY['docs'],'main','fixture-unused',$3)`, source, readOrg, boundary)
	store, err := infra.NewPostgresStoreFromURL(ctx, conn)
	require.NoError(t, err)
	defer store.Close()
	service, err = business.NewService(store)
	require.NoError(t, err)
	workContextSingleton.authority = store
	workContextSingleton.journal = store
	verified := auth.WithVerifiedDatabaseIdentity(ctx, readOwner, readOrg)
	var current *business.WorkContextAuthorityFacts
	require.NoError(t, store.WithSourceReadSnapshot(verified, readOrg, func(ctx context.Context) error {
		var err error
		current, err = store.ResolveWorkContextAuthority(ctx, readOrg, readOwner, "", []business.WorkContextPermission{{ResourceKind: "documents", Action: "read"}})
		return err
	}))
	facts.facts = current
	token := mint("documents", "read")
	result, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 1)
	require.Equal(t, source, result.Msg.Collections[0].SourceId)

	// Tenant size and irrelevant scope count cannot disable one readable source.
	hidden := uuid.NewString()
	exec(`INSERT INTO scope_nodes(id,org_id,scope_path,kind,label) VALUES($1,$2,'root.hidden','collection','Example Hidden')`, hidden, readOrg)
	exec(`INSERT INTO datasource_sources(id,org_id,provider,repo,paths,branch,credential_secret_ref,boundary_node_id)
 SELECT gen_random_uuid(),$1,'github','acme/hidden',ARRAY['docs'],'main','unused',$2 FROM generate_series(1,10001)`, readOrg, hidden)
	exec(`INSERT INTO scope_nodes(id,org_id,scope_path,kind,label)
 SELECT gen_random_uuid(),$1,('root.collection.child'||i)::ltree,'collection','Example Child' FROM generate_series(1,1001) i`, readOrg)
	result, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 1)

	// A concurrent grant swap never creates an owner/actor intersection.
	actor := uuid.NewString()
	require.NoError(t, store.WithSourceReadSnapshot(verified, readOrg, func(snapshot context.Context) error {
		_, _, err := store.SourceReadRevision(snapshot, readOrg, []string{readOwner, actor})
		require.NoError(t, err) // establish the snapshot before the concurrent commit
		exec(`BEGIN; DELETE FROM scope_grants WHERE org_id='` + readOrg + `'; INSERT INTO scope_grants(org_id,subject_id,subject_kind,scope_path,role_id) VALUES('` + readOrg + `','` + actor + `','principal','root.collection','` + role + `'); COMMIT;`)
		rows, err := store.ListReadableSourcesPage(snapshot, readOrg, []string{readOwner, actor}, "", 2)
		require.NoError(t, err)
		require.Empty(t, rows)
		ownerRows, err := store.ListReadableSourcesPage(snapshot, readOrg, []string{readOwner}, "", 2)
		require.NoError(t, err)
		require.Len(t, ownerRows, 1) // the original snapshot, despite the committed revocation
		return nil
	}))
	exec(`DELETE FROM scope_grants WHERE org_id=$1`, readOrg)
	exec(`INSERT INTO scope_grants(org_id,subject_id,subject_kind,scope_path,role_id) VALUES($1,$2,'principal','root.collection',$3)`, readOrg, readOwner, role)

	// Source mutation invalidates an existing cursor without rescanning the tenant.
	secondSource := uuid.NewString()
	exec(`INSERT INTO datasource_sources(id,org_id,provider,repo,paths,branch,credential_secret_ref,boundary_node_id) VALUES($1,$2,'github','acme/second',ARRAY['docs'],'main','unused',$3)`, secondSource, readOrg, boundary)
	first, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.NotEmpty(t, first.Msg.NextPageToken)
	next := sourceReadRequest(token)
	next.Msg.PageToken = first.Msg.NextPageToken
	exec(`UPDATE datasource_sources SET paths=ARRAY['public'] WHERE id=$1`, source)
	_, err = client.ListReadableSourceCollections(ctx, next)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Grant expiration changes no row, but still invalidates a partial enumeration.
	exec(`UPDATE scope_grants SET expires_at=now()+interval '1 second' WHERE org_id=$1`, readOrg)
	first, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	next = sourceReadRequest(token)
	next.Msg.PageToken = first.Msg.NextPageToken
	require.Eventually(t, func() bool {
		_, err := client.ListReadableSourceCollections(ctx, next)
		return connect.CodeOf(err) == connect.CodeInvalidArgument
	}, 3*time.Second, 50*time.Millisecond)
	exec(`UPDATE scope_grants SET expires_at=NULL WHERE org_id=$1`, readOrg)
	exec(`DELETE FROM scope_grants WHERE org_id=$1`, readOrg)
	result, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.Empty(t, result.Msg.Collections)
	// The new joined query preserves team inheritance and record-share semantics.
	team := uuid.NewString()
	exec(`INSERT INTO teams(id,org_id,name,slug,path) VALUES($1,$2,'Example Team','example-team','example-team')`, team, readOrg)
	exec(`INSERT INTO team_members(team_id,user_id,org_id) VALUES($1,$2,$3)`, team, readOwner, readOrg)
	checkSources := func(want int) {
		t.Helper()
		require.NoError(t, store.WithSourceReadSnapshot(verified, readOrg, func(snapshot context.Context) error {
			rows, err := store.ListReadableSourcesPage(snapshot, readOrg, []string{readOwner}, "", 10)
			require.NoError(t, err)
			require.Len(t, rows, want)
			return nil
		}))
	}
	exec(`INSERT INTO scope_grants(org_id,subject_id,subject_kind,scope_path,role_id) VALUES($1,$2,'team','root.collection',$3)`, readOrg, team, role)
	checkSources(2)
	exec(`DELETE FROM scope_grants WHERE org_id=$1`, readOrg)
	checkSources(0)
	exec(`UPDATE scope_nodes SET resource_type='documents',resource_id='example-record' WHERE id=$1`, boundary)
	exec(`INSERT INTO record_shares(org_id,resource_type,resource_id,subject_id,subject_kind,role_id) VALUES($1,'documents','example-record',$2,'team',$3)`, readOrg, team, role)
	checkSources(2)
	exec(`UPDATE record_shares SET expires_at=now()-interval '1 second' WHERE org_id=$1`, readOrg)
	checkSources(0)
	exec(`UPDATE record_shares SET expires_at=NULL, subject_kind='principal',subject_id=$2 WHERE org_id=$1`, readOrg, readOwner)
	checkSources(2)
	exec(`UPDATE role_permissions SET action='write' WHERE role_id=$1`, role)
	checkSources(0)

}
