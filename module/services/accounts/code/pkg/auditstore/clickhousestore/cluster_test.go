package clickhousestore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The adapter against a ClickHouse cluster, when one is reachable: the same
// server as server_test.go with a cluster of at least two replicas, declared
// in its remote_servers and coordinated by Keeper, and its name in
//
//	AUDIT_CLICKHOUSE_TEST_CLUSTER=<cluster>
//
// It is skipped otherwise. The database is created and dropped on the cluster.

// clusterDatabase creates a database for one test on the cluster, connects to
// it, and returns the cluster's name with it.
func clusterDatabase(t *testing.T) (conn driver.Conn, database, cluster string) {
	t.Helper()
	cluster = strings.TrimSpace(os.Getenv("AUDIT_CLICKHOUSE_TEST_CLUSTER"))
	dsn := strings.TrimSpace(os.Getenv("AUDIT_CLICKHOUSE_TEST_DSN"))
	if cluster == "" || dsn == "" {
		t.Skip("AUDIT_CLICKHOUSE_TEST_CLUSTER and AUDIT_CLICKHOUSE_TEST_DSN are not both set; see the comment above")
	}
	ctx := context.Background()
	options, err := clickhouse.ParseDSN(dsn)
	require.NoError(t, err)
	admin, err := clickhouse.Open(options)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	database = "audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, admin.Exec(ctx, "CREATE DATABASE "+database+" ON CLUSTER '"+cluster+"'"))
	t.Cleanup(func() { _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+database+" ON CLUSTER '"+cluster+"' SYNC") })
	scoped := *options
	scoped.Auth.Database = database
	conn, err = clickhouse.Open(&scoped)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, database, cluster
}

func TestServerClusterInsertsWaitForAQuorumAndReadsAreSequential(t *testing.T) {
	conn, database, cluster := clusterDatabase(t)
	ctx := context.Background()

	store := serverStore(t, conn, database, func(cfg *Config) { cfg.Cluster = cluster })
	require.NoError(t, store.Ensure(ctx))
	for _, table := range []string{EventsTable, DetailsTable} {
		var engine string
		require.NoError(t, store.queryRow(ctx, "SELECT engine FROM system.tables WHERE database = currentDatabase() AND name = '"+table+"'", nil, &engine))
		require.Equal(t, "ReplicatedMergeTree", engine, table)
	}

	org, actor := uuid.NewString(), uuid.NewString()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	appendBatch(t, store, testDeployment,
		newRecord(t, org, actor, business.EventAuthLogin, "", at, business.RetentionSecurity, map[string]any{"method": "password"}),
		newRecord(t, org, actor, business.EventDocumentRead, "doc-1", at.Add(-time.Hour), business.RetentionContent, map[string]any{"boundary": "b-1"}),
	)
	read := business.AuditRead{Scope: business.OrganizationAuditScope(org), Query: business.AuditQuery{OrgID: org}}
	listed, _, err := store.ListAuditEvents(ctx, read)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.NotNil(t, listed[1].Payload, "the content-class event's details are joined from the replicated details table")

	// The server saw the quorum on the inserts, and the sequential reading on
	// the reads, whatever the user's profile says.
	require.NoError(t, conn.Exec(ctx, "SYSTEM FLUSH LOGS"))
	var inserts, reads uint64
	require.NoError(t, store.queryRow(ctx, "SELECT count() FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Insert' "+
		"AND current_database = currentDatabase() AND Settings['insert_quorum'] = 'auto' AND Settings['insert_quorum_parallel'] = '0'", nil, &inserts))
	require.EqualValues(t, 2, inserts, "one quorum insert per table")
	require.NoError(t, store.queryRow(ctx, "SELECT count() FROM system.query_log WHERE type = 'QueryFinish' AND query_kind = 'Select' "+
		"AND current_database = currentDatabase() AND Settings['select_sequential_consistency'] = '1' AND query LIKE '%"+EventsTable+"%'", nil, &reads))
	require.Positive(t, reads)
}

// A table that is already there is accepted for its columns and window, and on
// a cluster that is not enough: a MergeTree table lives on one node, so
// insert_quorum has nothing to wait for. A table found on a cluster is refused
// unless it is replicated, each table by itself, and one server keeps
// accepting a MergeTree table.
func TestServerClusterRefusesAnExistingTableThatIsNotReplicated(t *testing.T) {
	conn, database, cluster := clusterDatabase(t)
	ctx := context.Background()
	single := serverStore(t, conn, database)
	clustered := serverStore(t, conn, database, func(cfg *Config) { cfg.Cluster = cluster })

	// Both tables exist as MergeTree, the way a one-server deployment made them.
	require.NoError(t, single.Ensure(ctx))
	require.NoError(t, single.Ensure(ctx), "one server accepts the MergeTree tables it made")
	err := clustered.Ensure(ctx)
	require.ErrorContains(t, err, "table "+database+".audit_events: engine MergeTree is not replicated")
	require.ErrorContains(t, err, `cluster "`+cluster+`"`)

	// Replace the events table with the replicated one the store writes on a
	// cluster; the details table is still a MergeTree one. The MergeTree tables
	// exist on the one node the connection reached, so a drop on the cluster
	// finds them missing on the others.
	drop := func(table string) {
		require.NoError(t, conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" ON CLUSTER '"+cluster+"' SYNC"))
	}
	drop(EventsTable)
	statements := clustered.CreateStatements()
	require.NoError(t, conn.Exec(ctx, statements[0]))
	err = clustered.Ensure(ctx)
	require.ErrorContains(t, err, "table "+database+".audit_event_details: engine MergeTree is not replicated")
	require.NotContains(t, err.Error(), EventsTable+":", "the replicated events table is not the one refused")

	// Replaced too, every table is replicated, and the cluster accepts them.
	drop(DetailsTable)
	require.NoError(t, conn.Exec(ctx, statements[1]))
	require.NoError(t, clustered.Ensure(ctx), "a replicated table that already exists is accepted")
	for _, table := range []string{EventsTable, DetailsTable} {
		var engine string
		require.NoError(t, clustered.queryRow(ctx, "SELECT engine FROM system.tables WHERE database = currentDatabase() AND name = '"+table+"'", nil, &engine))
		require.Equal(t, "ReplicatedMergeTree", engine, table)
	}
}
