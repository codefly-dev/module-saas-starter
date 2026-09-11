//go:build integration

package adapters

import (
	"context"
	"os"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/infra"
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
	_, _, client, mint := sourceReadFixture(t)
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
	})
	exec(`INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'member')`, readOrg, readOwner)
	exec(`INSERT INTO roles(id,name,org_id) VALUES($1,'reader',$2)`, role, readOrg)
	exec(`INSERT INTO role_permissions(role_id,resource,action) VALUES($1,'documents','read')`, role)
	exec(`INSERT INTO scope_nodes(id,org_id,scope_path,kind,label) VALUES($1,$2,'root.collection','collection','Example Collection')`, boundary, readOrg)
	exec(`INSERT INTO scope_grants(org_id,subject_id,subject_kind,scope_path,role_id) VALUES($1,$2,'principal','root.collection',$3)`, readOrg, readOwner, role)
	exec(`INSERT INTO datasource_sources(id,org_id,provider,repo,paths,branch,credential_secret_ref,boundary_node_id) VALUES($1,$2,'github','acme/handbook',ARRAY['docs'],'main','fixture-unused',$3)`, source, readOrg, boundary)
	store, err := infra.NewPostgresStoreFromURL(ctx, conn)
	require.NoError(t, err)
	defer store.Close()
	service, err = business.NewService(store)
	require.NoError(t, err)
	result, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "read")))
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 1)
	require.Equal(t, source, result.Msg.Collections[0].SourceId)
	exec(`DELETE FROM scope_grants WHERE org_id=$1`, readOrg)
	result, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "read")))
	require.NoError(t, err)
	require.Empty(t, result.Msg.Collections)
}
