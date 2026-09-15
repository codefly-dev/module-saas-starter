//go:build integration

package adapters

import (
	"context"
	"os"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"time"

	"connectrpc.com/connect"

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
	service.SetModuleCapabilities(nil, nil, business.ModulePrincipalRegistry{
		business.ModulePrincipalID("documents"): {Prefix: "documents", Resources: []string{"documents"}},
		business.ModulePrincipalID("rows"):      {Prefix: "rows", Resources: []string{"rows"}},
	})
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
	token := mint("documents", "documents", "read")
	result, err := client.ListReadableSourceCollections(ctx, sourceReadRequest(token))
	require.NoError(t, err)
	require.Len(t, result.Msg.Collections, 1)
	require.Equal(t, source, result.Msg.Collections[0].SourceId)
	require.Equal(t, "Example Collection", result.Msg.Collections[0].BoundaryLabel)
	// This viewer is an ordinary org member, so the details an administrator's
	// collection view carries are withheld rather than silently absent.
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_WITHHELD, result.Msg.Collections[0].GrantDisclosure)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_WITHHELD, result.Msg.Collections[0].Sync.RequesterDisclosure)

	// Widening the viewer's own authority — still no organization-admin tenancy —
	// is what discloses them.
	exec(`UPDATE datasource_sources SET last_ingested_at=now(), last_ingested_commit='0e57a1c', last_delivery_id='delivery-1' WHERE id=$1`, source)
	// The typed registry is seeded at service boot, which this harness does not run.
	exec(`INSERT INTO audit_event_types(name,category,owner) VALUES($1,'system','accounts') ON CONFLICT(name) DO NOTHING`, string(business.EventDatasourceSourceSynced))
	exec(`INSERT INTO audit_events(id,event_type,actor_id,actor_type,resource,resource_id,org_id)
 VALUES(gen_random_uuid(),$1,$2,'user','datasource',$3,$4)`, string(business.EventDatasourceSourceSynced), readOwner, source, readOrg)
	inspector := uuid.NewString()
	exec(`INSERT INTO roles(id,name,org_id) VALUES($1,'inspector',$2)`, inspector, readOrg)
	exec(`INSERT INTO role_permissions(role_id,resource,action) VALUES($1,'roles','read'),($1,'audit','read')`, inspector)
	exec(`INSERT INTO role_assignments(org_id,subject_id,subject_kind,role_id) VALUES($1,$2,'principal',$3)`, readOrg, readOwner, inspector)
	resolve := func(permissions ...business.WorkContextPermission) {
		t.Helper()
		require.NoError(t, store.WithSourceReadSnapshot(verified, readOrg, func(ctx context.Context) error {
			current, err := store.ResolveWorkContextAuthority(ctx, readOrg, readOwner, "", permissions)
			facts.facts = current
			return err
		}))
	}
	inspect := []business.WorkContextPermission{{ResourceKind: "documents", Action: "read"}, {ResourceKind: "roles", Action: "read"}, {ResourceKind: "audit", Action: "read"}}
	resolve(inspect...)
	detailed := mint("documents", "documents", "read", "roles:read", "audit:read")
	result, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(detailed))
	require.NoError(t, err)
	collection := result.Msg.Collections[0]
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, collection.GrantDisclosure)
	require.Len(t, collection.ReadGrants, 1)
	// Registration seeds a human principal's display name from its primary email.
	require.Equal(t, "reader@example.com", collection.ReadGrants[0].SubjectLabel)
	require.Equal(t, "reader", collection.ReadGrants[0].RoleName)
	require.False(t, collection.ReadGrants[0].Inherited)
	require.Equal(t, gen.SourceSyncStage_SOURCE_SYNC_STAGE_CHANGES_ENQUEUED, collection.Sync.Stage)
	require.Equal(t, "0e57a1c", collection.Sync.Revision)
	require.Equal(t, "delivery-1", collection.Sync.Trigger)
	require.Equal(t, gen.MetadataDisclosure_METADATA_DISCLOSURE_DISCLOSED, collection.Sync.RequesterDisclosure)
	require.Equal(t, "reader@example.com", collection.Sync.RequestedByLabel)
	// The request and the enqueue are different occurrences, and the projection
	// keeps them apart rather than reporting one timestamp for both.
	require.NotEqual(t, collection.Sync.At.AsTime(), collection.Sync.RequestedAt.AsTime())

	// Only the NEWEST request is reported. This is the whole point of the lookup
	// and the one thing a single-row fixture cannot show: with several requests
	// on one source, an ordering that lost this would still look correct.
	later := uuid.NewString()
	exec(`INSERT INTO users(uuid,primary_email) VALUES($1,'second-operator@example.com')`, later)
	exec(`INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'member')`, readOrg, later)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE uuid=$1`, later)
		_, _ = pool.Exec(ctx, `DELETE FROM principals WHERE id=$1`, later)
	})
	// The winner is written last at now() and the losers backdated, because
	// audit_events is append-only: the row the block above wrote is still there
	// and is itself recent, so the newest row has to be one this block controls.
	exec(`INSERT INTO audit_events(id,event_type,actor_id,actor_type,resource,resource_id,org_id,created_at)
 VALUES(gen_random_uuid(),$1,$2,'user','datasource',$3,$4,now()-interval '1 hour'),
       (gen_random_uuid(),$1,$2,'user','datasource',$3,$4,now()-interval '2 hours'),
       (gen_random_uuid(),$1,$5,'user','datasource',$3,$4,now())`,
		string(business.EventDatasourceSourceSynced), readOwner, source, readOrg, later)
	// Adding a member moved the organization's authorization revision.
	resolve(inspect...)
	result, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "documents", "read", "roles:read", "audit:read")))
	require.NoError(t, err)
	require.Equal(t, "second-operator@example.com", result.Msg.Collections[0].Sync.RequestedByLabel,
		"the most recent request must win regardless of insertion order")
	newest := result.Msg.Collections[0].Sync.RequestedAt.AsTime()
	require.True(t, time.Since(newest) < 2*time.Minute, "reported request %s is not the newest", newest)

	// A grant on an ancestor of the boundary confers read and is reported as
	// inherited; the administrator's view and this one agree on the set.
	grandparent := uuid.NewString()
	exec(`INSERT INTO scope_nodes(id,org_id,scope_path,kind,label) VALUES($1,$2,'root','collection','Example Root')`, grandparent, readOrg)
	exec(`INSERT INTO scope_grants(org_id,subject_id,subject_kind,scope_path,role_id) VALUES($1,$2,'principal','root',$3)`, readOrg, readOwner, role)
	resolve(inspect...)
	result, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(mint("documents", "documents", "read", "roles:read", "audit:read")))
	require.NoError(t, err)
	inherited := map[string]bool{}
	for _, grant := range result.Msg.Collections[0].ReadGrants {
		inherited[grant.ScopePath] = grant.Inherited
	}
	require.Equal(t, map[string]bool{"root.collection": false, "root": true}, inherited)
	exec(`DELETE FROM scope_grants WHERE org_id=$1 AND scope_path='root'`, readOrg)

	// Revoking the permission the disclosure rests on fails the whole call rather
	// than quietly narrowing the next page, because the capability sealed it.
	exec(`DELETE FROM role_assignments WHERE org_id=$1 AND role_id=$2`, readOrg, inspector)
	_, err = client.ListReadableSourceCollections(ctx, sourceReadRequest(detailed))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	exec(`INSERT INTO role_assignments(org_id,subject_id,subject_kind,role_id) VALUES($1,$2,'principal',$3)`, readOrg, readOwner, inspector)

	// Another tenant's collections and their metadata are outside the snapshot.
	require.NoError(t, store.WithSourceReadSnapshot(verified, readOrg, func(snapshot context.Context) error {
		grants, err := store.ReadableCollectionGrants(snapshot, uuid.NewString(), []string{boundary}, []string{"documents"})
		require.NoError(t, err)
		require.Empty(t, grants)
		requests, err := store.LatestSourceSyncRequests(snapshot, uuid.NewString(), []string{source})
		require.NoError(t, err)
		require.Empty(t, requests)
		return nil
	}))
	resolve(business.WorkContextPermission{ResourceKind: "documents", Action: "read"})
	token = mint("documents", "documents", "read")

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
		rows, err := store.ListReadableSourcesPage(snapshot, readOrg, []string{readOwner, actor}, []string{"documents"}, "", 2)
		require.NoError(t, err)
		require.Empty(t, rows)
		ownerRows, err := store.ListReadableSourcesPage(snapshot, readOrg, []string{readOwner}, []string{"documents"}, "", 2)
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
	checkSourcesUnder := func(resources []string, want int) {
		t.Helper()
		require.NoError(t, store.WithSourceReadSnapshot(verified, readOrg, func(snapshot context.Context) error {
			rows, err := store.ListReadableSourcesPage(snapshot, readOrg, []string{readOwner}, resources, "", 10)
			require.NoError(t, err)
			require.Len(t, rows, want)
			return nil
		}))
	}
	checkSources := func(want int) { t.Helper(); checkSourcesUnder([]string{"documents"}, want) }
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

	// A collection whose content another module governs is read by that module
	// and by no other: the resource type is the caller's declaration, and an
	// undeclared composition reads nothing at all.
	exec(`UPDATE role_permissions SET resource='rows',action='read' WHERE role_id=$1`, role)
	exec(`UPDATE scope_nodes SET resource_type='rows' WHERE id=$1`, boundary)
	exec(`UPDATE record_shares SET resource_type='rows' WHERE org_id=$1`, readOrg)
	checkSourcesUnder([]string{"rows"}, 2)
	checkSourcesUnder([]string{"documents"}, 0)
	checkSourcesUnder(nil, 0)
	// The standing-grant branch answers the same way, independently of shares.
	exec(`DELETE FROM record_shares WHERE org_id=$1`, readOrg)
	exec(`INSERT INTO scope_grants(org_id,subject_id,subject_kind,scope_path,role_id) VALUES($1,$2,'principal','root.collection',$3)`, readOrg, readOwner, role)
	checkSourcesUnder([]string{"rows"}, 2)
	checkSourcesUnder([]string{"documents"}, 0)

	// A wildcard permission — what migration 4 seeds the built-in admin role with —
	// matches every resource type on its own, so it satisfies whichever type the
	// caller declared. The declaration is then the only bound left, and an empty
	// one has to be refused explicitly: without that, a module that declared no
	// content would read every collection a wildcard role covers.
	exec(`UPDATE role_permissions SET resource='*',action='*' WHERE role_id=$1`, role)
	checkSourcesUnder([]string{"rows"}, 2)
	checkSourcesUnder([]string{"documents"}, 2)
	checkSourcesUnder([]string{}, 0)
	checkSourcesUnder(nil, 0)
}
