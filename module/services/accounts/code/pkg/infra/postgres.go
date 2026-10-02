package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/infra/internal/txbind"
	"accounts/pkg/infra/storetx"

	codefly "github.com/codefly-dev/sdk-go"
	scopedpostgres "github.com/codefly-dev/service-postgres/libs/go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"

	"github.com/jackc/pgx/v5/pgxpool"

	"accounts/pkg/business"
)

type Close func()

type PostgresStore struct {
	Close
	// pool carries request traffic. In the served process its login's whole
	// reachable authority is app_tenant (see requireRequestLoginAuthority), so
	// nothing on a request connection can select a wider role or use a privilege
	// of its own.
	pool *pgxpool.Pool
	// controlPlane is the pool WithControlPlane assumes app_control_plane on.
	// In the served process it authenticates as its own login, distinct from
	// the request login; a single-credential tooling store (NewPostgresStoreFromURL)
	// points it at pool.
	controlPlane *pgxpool.Pool
	database     *scopedpostgres.Factory
}

// beforeConnectHook resolves a fresh password for every pool connection attempt.
// The request and control-plane pools use this pgx hook; the scoped reader/writer
// capability pools use service-postgres WithAccessTokenProvider and keep their
// pools private.
type beforeConnectHook = func(context.Context, *pgx.ConnConfig) error

const scopedBoundaryOperationTimeout = 5 * time.Second

// The Codefly Postgres connection capabilities the served process consumes, each
// a distinct login. The migration owner is none of them: it stays private to the
// store's bootstrap Job.
const (
	readOnlyConnectionKey = "read-only-connection"
	// readWriteConnectionKey is the request login. The store declares app_tenant
	// as its only runtime-read-write role, so this login reaches nothing else.
	readWriteConnectionKey = "read-write-connection"
	// controlPlaneConnectionKey is the login the store declares for cross-tenant
	// work: app_control_plane and the worker roles, never request traffic.
	controlPlaneConnectionKey = "control-plane-connection"
)

func storeConnection(ctx context.Context, key string) (string, error) {
	return codefly.For(ctx).Service("store").Secret("postgres", key)
}

func NewPostgresStore(ctx context.Context) (*PostgresStore, error) {
	w := wool.Get(ctx).In("NewPostgresStore")
	readOnlyConnection, err := storeConnection(ctx, readOnlyConnectionKey)
	if err != nil {
		return nil, w.Wrapf(err, "failed to get read-only connection string")
	}
	readWriteConnection, err := storeConnection(ctx, readWriteConnectionKey)
	if err != nil {
		return nil, w.Wrapf(err, "failed to get read-write connection string")
	}
	controlPlaneConnection, err := storeConnection(ctx, controlPlaneConnectionKey)
	if err != nil {
		return nil, w.Wrapf(err, "failed to get control-plane connection string")
	}

	return NewPostgresStoreWithCapabilities(ctx, readOnlyConnection, readWriteConnection, controlPlaneConnection)
}

// NewPostgresStoreWithCapabilities composes the production boundary from
// primitive-projected connection secrets: the scoped reader/writer pools, the
// request pool, and a separate control-plane pool. It preserves verified
// identity, distinct roles, startup pings and the rotating credential hook.
//
// The request pool selects no role. Its login starts as app_tenant — the
// store's first runtime-read-write role is the login's session default — and
// can reach nothing wider, so there is no step-down for a request connection to
// undo. Every new request connection refuses a login that can reach any other
// role or holds a privilege app_tenant does not (of the classes
// loginPrivilegesQuery judges), and startup opens the first one, so a
// deployment that hands both capabilities the same credential fails here
// rather than serving with a request path that can leave the tenant filter. A
// released request connection returns to app_tenant with no request binding or
// is discarded. Startup also refuses a control-plane capability that
// shares a login with either other capability or cannot assume
// app_control_plane, and every new control-plane connection, the first one
// included, is judged as a request connection is — against the roles the store
// declares for that login — so a misprovisioned control plane fails here rather
// than on the first registration or login.
func NewPostgresStoreWithCapabilities(ctx context.Context, readOnlyConnection, readWriteConnection, controlPlaneConnection string) (*PostgresStore, error) {
	w := wool.Get(ctx).In("NewPostgresStoreWithCapabilities")

	// Every boundary resolves the rotating credential for its physical login.
	// A nil provider preserves local passwords embedded in the capability URLs.
	provider := tokenFileAccessTokenProvider()

	readerConfig, err := configureConnection(readOnlyConnection, accessTokenBeforeConnect(provider))
	if err != nil {
		return nil, w.Wrapf(err, "failed to parse read-only connection string")
	}
	requestConfig, err := configureConnection(readWriteConnection, accessTokenBeforeConnect(provider))
	if err != nil {
		return nil, w.Wrapf(err, "failed to parse read-write connection string")
	}
	controlPlaneConfig, err := configureConnection(controlPlaneConnection, accessTokenBeforeConnect(provider))
	if err != nil {
		return nil, w.Wrapf(err, "failed to parse control-plane connection string")
	}
	if err := requireDistinctControlPlaneLogin(readerConfig, requestConfig, controlPlaneConfig); err != nil {
		return nil, err
	}
	requestConfig.AfterConnect = requireRequestLoginAuthority
	requestConfig.AfterRelease = returnsToTenantRole
	controlPlaneConfig.AfterConnect = requireControlPlaneLoginAuthority
	controlPlaneConfig.AfterRelease = returnsToSessionRole

	// The request and control-plane capabilities are judged before the scoped
	// boundary opens. Each names the role a misprovisioned login reaches, where
	// the scoped pools' own restricted-session policy can only report that the
	// session failed it; judging these first means startup reports the most
	// specific refusal it has rather than the first one in construction order.
	requestPool, err := pgxpool.NewWithConfig(ctx, requestConfig)
	if err != nil {
		return nil, w.Wrapf(err, "failed to open request pool")
	}
	// AfterConnect judges every connection before the pool hands it out, so
	// opening the first one now fails startup on a misprovisioned request login
	// rather than the first request.
	if err := requestPool.Ping(ctx); err != nil {
		requestPool.Close()
		return nil, fmt.Errorf("open request Postgres connection: %w", err)
	}
	controlPlanePool, err := pgxpool.NewWithConfig(ctx, controlPlaneConfig)
	if err != nil {
		requestPool.Close()
		return nil, w.Wrapf(err, "failed to open control-plane pool")
	}
	if err := verifyControlPlaneLogin(ctx, controlPlanePool); err != nil {
		controlPlanePool.Close()
		requestPool.Close()
		return nil, err
	}
	if err := verifyReaderLogin(ctx, readerConfig); err != nil {
		controlPlanePool.Close()
		requestPool.Close()
		return nil, err
	}

	database, closeDatabase, err := openScopedBoundary(ctx, readOnlyConnection, readWriteConnection, provider)
	if err != nil {
		controlPlanePool.Close()
		requestPool.Close()
		return nil, w.Wrapf(err, "failed to open authenticated Postgres boundary")
	}
	return &PostgresStore{
		Close: func() {
			controlPlanePool.Close()
			requestPool.Close()
			closeDatabase()
		},
		pool:         requestPool,
		controlPlane: controlPlanePool,
		database:     database,
	}, nil
}

// requireDistinctControlPlaneLogin refuses a control-plane capability that
// authenticates as the request login or the reader. Behind a local identity
// proxy the socket is the identity, so the control plane also needs a socket of
// its own.
func requireDistinctControlPlaneLogin(reader, request, controlPlane *pgxpool.Config) error {
	others := []struct {
		name   string
		config *pgxpool.Config
	}{{"request", request}, {"read-only", reader}}
	for _, other := range others {
		if other.config.ConnConfig.User == controlPlane.ConnConfig.User {
			return fmt.Errorf("%s and control-plane Postgres capabilities must use distinct logins; both authenticate as %q", other.name, controlPlane.ConnConfig.User)
		}
	}
	profile, err := DatabaseTransportProfile()
	if err != nil {
		return err
	}
	if profile != "local-identity-proxy" {
		return nil
	}
	for _, other := range others {
		if other.config.ConnConfig.Host == controlPlane.ConnConfig.Host {
			return fmt.Errorf("%s and control-plane require distinct private identity sockets", other.name)
		}
	}
	return nil
}

// controlPlaneLoginRoles are the roles the store declares for its control-plane
// login: app_control_plane and the three worker roles.
var controlPlaneLoginRoles = []string{
	controlPlaneDatabaseRole,
	billingWorkerDatabaseRole,
	webhookProjectionDatabaseRole,
	jobWorkerDatabaseRole,
}

// verifyControlPlaneLogin fails closed unless the control-plane login can assume
// app_control_plane. The pool opens lazily, so without this probe a capability
// whose login lacks the membership would pass startup and fail on the first
// registration, login or identity resolution instead. Opening the probe's
// connection also runs the pool's AfterConnect, which refuses a login that
// reaches more than the store declares for it (requireControlPlaneLoginAuthority).
func verifyControlPlaneLogin(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("open control-plane Postgres connection: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // the probe changes nothing; rolling back only ends it
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
		return fmt.Errorf("control-plane Postgres login cannot assume %s: %w", controlPlaneDatabaseRole, err)
	}
	return nil
}

// verifyReaderLogin fails closed unless the read-only capability's login is a
// read-only one: requireReaderLoginAuthority refuses a reader that reaches any
// role, holds an attribute or ownership that skips row-level security, or holds
// a privilege beyond the SELECTs it was granted.
//
// This is a startup judgement on a connection of the reader's own capability,
// not on a disposable substitute: it authenticates with the same credential,
// through the same transport profile and rotation hook, as the scoped reader.
//
// The same judgement now also runs on every scoped reader connection and every
// checkout, through the primitive's connection-policy seam
// (readerConnectionPolicy). This startup call is not redundant: it is ordered
// BEFORE the scoped boundary opens, so a misprovisioned reader is reported by
// the judgement that names the offending role, attribute or grant rather than
// by a pool refusing to hand out its first connection.
func verifyReaderLogin(ctx context.Context, readerConfig *pgxpool.Config) error {
	config := readerConfig.Copy()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("open read-only Postgres connection: %w", err)
	}
	defer pool.Close()
	return requireReaderLoginAuthority(ctx, pool)
}

// requestReleaseTimeout bounds the reset a request connection runs as it
// returns to the pool.
const requestReleaseTimeout = 5 * time.Second

// sessionReset returns a released connection to its login's session defaults
// and reads back what it returned to. RESET ROLE restores the per-role default
// role, app_tenant for the request login. RESET ALL restores every other
// session setting — app.current_org_id and app.current_user_id included, which
// RESET ROLE leaves in place — and does not itself reset the role, so both are
// needed. Neither deallocates prepared statements, so the statements pgx caches
// per connection stay valid on the reused connection, which DISCARD ALL would
// deallocate. DISCARD TEMP drops any temporary table the borrower left: pg_temp
// is searched before every other schema, so one left behind would shadow the
// next borrower's unqualified table names, and RESET ALL does not touch it.
// Unlike DISCARD ALL it may run inside the implicit transaction a multi-statement
// message opens. The four statements travel in one simple-protocol message.
const sessionReset = `RESET ROLE; RESET ALL; DISCARD TEMP; SELECT current_user::text,
	coalesce(current_setting('app.current_org_id', true), ''),
	coalesce(current_setting('app.current_user_id', true), '')`

// resetSession runs sessionReset on a released connection. It returns the role
// the session came back to, and false if the reset failed or left a request
// binding behind.
func resetSession(conn *pgx.Conn) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), requestReleaseTimeout)
	defer cancel()
	results, err := conn.PgConn().Exec(ctx, sessionReset).ReadAll()
	if err != nil || len(results) != 4 || len(results[3].Rows) != 1 || len(results[3].Rows[0]) != 3 {
		return "", false
	}
	row := results[3].Rows[0]
	return string(row[0]), len(row[1]) == 0 && len(row[2]) == 0
}

// returnsToTenantRole is the request pool's AfterRelease. A session-level change
// a borrower leaves behind would reach the next borrower until the connection is
// recycled: `SET ROLE NONE` hands it the bare login, and a session-level
// app.current_org_id or app.current_user_id hands it the previous caller's
// scope, which every un-wrapped read runs under. The reset clears both; a
// connection that does not come back as app_tenant with no binding is
// discarded rather than reused.
func returnsToTenantRole(conn *pgx.Conn) bool {
	role, clean := resetSession(conn)
	return clean && role == tenantDatabaseRole
}

// sessionRoleKey names the per-connection record of the role a control-plane
// or worker connection started as, which requireControlPlaneLoginAuthority
// writes when it admits the connection.
const sessionRoleKey = "accounts.session-role"

// returnsToSessionRole is the AfterRelease of the control-plane pool and of
// every worker pool. They share one login, whose session default is whichever
// declared role the provisioning made it, so the reset must bring the
// connection back to the role it started as rather than to a fixed one. A
// borrower's session-level `SET ROLE` — to a worker role, or `NONE` for the bare
// login — or session-level request binding would otherwise reach the next
// borrower: every control-plane transaction assumes app_control_plane only with
// SET LOCAL, and a worker pool's PrepareConn selects its role again on top of
// whatever the connection holds. The reset clears both, and a connection that
// does not come back to its starting role with no binding — or whose starting
// role was never recorded — is discarded rather than reused.
func returnsToSessionRole(conn *pgx.Conn) bool {
	started, ok := conn.PgConn().CustomData()[sessionRoleKey].(string)
	if !ok {
		return false
	}
	role, clean := resetSession(conn)
	return clean && role == started
}

// loginRolesQuery returns one row per role the session's login can reach — the
// login included — with every attribute and ownership that would let a session
// holding that role skip or rewrite RLS. A role counts as reachable if any
// chain of membership edges leads to it, whatever each membership's INHERIT or
// SET option, and also wherever pg_has_role says the login holds it, through
// inherited privileges (USAGE) or any membership (MEMBER). pg_has_role also sees
// the memberships no pg_auth_members edge records: a superuser holds every
// role, and owning a database makes a role an implicit member of
// pg_database_owner, which owns the public schema on PostgreSQL 15 and later.
const loginRolesQuery = `
	WITH RECURSIVE reachable(oid) AS (
		SELECT role.oid
		FROM pg_catalog.pg_roles role
		WHERE role.rolname = session_user
		   OR pg_catalog.pg_has_role(session_user, role.oid, 'USAGE')
		   OR pg_catalog.pg_has_role(session_user, role.oid, 'MEMBER')
		UNION
		SELECT membership.roleid
		FROM pg_catalog.pg_auth_members membership
		JOIN reachable ON membership.member = reachable.oid
	)
	SELECT current_user::text,
	       session_user::text,
	       role.rolname::text,
	       role.rolsuper,
	       role.rolbypassrls,
	       role.rolcreaterole,
	       role.rolcreatedb,
	       role.rolreplication,
	       EXISTS (SELECT 1 FROM pg_catalog.pg_class WHERE relowner = role.oid),
	       EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspowner = role.oid),
	       EXISTS (SELECT 1 FROM pg_catalog.pg_proc WHERE proowner = role.oid),
	       EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datdba = role.oid)
	FROM reachable
	JOIN pg_catalog.pg_roles role ON role.oid = reachable.oid
	ORDER BY role.rolname`

// loginPrivilegesQuery returns every privilege the session's login holds on an
// application table, column, sequence or function, on the current database, on
// an application schema or on a server parameter, that none of the roles named
// by $1 holds, and every default-privilege entry that grants to the login or is
// defined for objects it creates. Privileges the login holds through one of $1
// or PUBLIC cancel out, so what remains was granted to the login itself (or to
// a role it reaches). Such a privilege survives `SET ROLE NONE`, and on a table
// without row-level security nothing filters what it reads or writes; a default
// privilege hands one to the login with the next migration. `CREATE` on the
// database or a schema lets the bare login create a schema or table, without
// row-level security, that a request session's search_path resolves an
// unqualified table name to; `TEMPORARY` does the same through pg_temp, which
// is searched first. Only columns with a column-level ACL can hold more than
// their table, so only those are compared column by column.
//
// The system catalog is judged too, wherever something has granted on it: a
// direct `GRANT EXECUTE ON FUNCTION pg_read_file(text)` or `GRANT SELECT ON
// pg_authid` to the login reads the server's files or every role's password
// hash from any session, whatever role it has selected. A catalog relation or
// function whose ACL is still the default (NULL) holds nothing a grant could
// have added — a relation's default is its owner alone, a function's is PUBLIC,
// which every role shares — so only those with an ACL are compared; a catalog
// column is compared, like any other, once it has an ACL of its own. The other
// pg_ schemas and information_schema stay out.
//
// A parameter privilege is judged the same way: `GRANT SET ON PARAMETER` on a
// superuser-only setting such as session_replication_role lets the login switch
// off every trigger, and `GRANT ALTER SYSTEM ON PARAMETER` on one such as
// archive_command has the server run a command of its choosing. Only parameters
// something has granted on have a row in pg_parameter_acl; the rest grant
// nothing to a non-superuser, so only those rows are compared.
//
// The remaining privilege classes are not judged: USAGE on a type, domain,
// language, foreign-data wrapper or foreign server, CREATE on a tablespace, and
// large-object privileges. None reads or writes a tenant row on its own
// (DATABASE_AUTHORITY.md says why for each).
const loginPrivilegesQuery = `
	WITH login AS (
		SELECT oid FROM pg_catalog.pg_roles WHERE rolname = session_user
	), declared AS (
		SELECT unnest($1::name[]) AS rolname
	), relation AS (
		SELECT rel.oid, rel.relkind, format('%I.%I', namespace.nspname, rel.relname) AS name,
		       namespace.nspname = 'pg_catalog' AS catalog, rel.relacl IS NOT NULL AS granted
		FROM pg_catalog.pg_class rel
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid = rel.relnamespace
		WHERE rel.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
		  AND (namespace.nspname = 'pg_catalog'
		       OR (namespace.nspname <> 'information_schema' AND namespace.nspname NOT LIKE 'pg\_%'))
	)
	SELECT privilege || ' on table ' || relation.name
	FROM relation
	CROSS JOIN unnest(ARRAY['SELECT', 'INSERT', 'UPDATE', 'DELETE', 'TRUNCATE', 'REFERENCES', 'TRIGGER']) AS privilege
	WHERE relation.relkind <> 'S'
	  AND (NOT relation.catalog OR relation.granted)
	  AND pg_catalog.has_table_privilege(session_user, relation.oid, privilege)
	  AND NOT EXISTS (SELECT 1 FROM declared WHERE pg_catalog.has_table_privilege(declared.rolname, relation.oid, privilege))
	UNION ALL
	SELECT privilege || ' on column ' || relation.name || '.' || quote_ident(attribute.attname)
	FROM relation
	JOIN pg_catalog.pg_attribute attribute ON attribute.attrelid = relation.oid
	CROSS JOIN unnest(ARRAY['SELECT', 'INSERT', 'UPDATE', 'REFERENCES']) AS privilege
	WHERE relation.relkind <> 'S'
	  AND attribute.attnum > 0
	  AND NOT attribute.attisdropped
	  AND attribute.attacl IS NOT NULL
	  AND pg_catalog.has_column_privilege(session_user, relation.oid, attribute.attnum, privilege)
	  AND NOT EXISTS (SELECT 1 FROM declared WHERE pg_catalog.has_column_privilege(declared.rolname, relation.oid, attribute.attnum, privilege))
	UNION ALL
	SELECT privilege || ' on sequence ' || relation.name
	FROM relation
	CROSS JOIN unnest(ARRAY['USAGE', 'SELECT', 'UPDATE']) AS privilege
	WHERE relation.relkind = 'S'
	  AND pg_catalog.has_sequence_privilege(session_user, relation.oid, privilege)
	  AND NOT EXISTS (SELECT 1 FROM declared WHERE pg_catalog.has_sequence_privilege(declared.rolname, relation.oid, privilege))
	UNION ALL
	SELECT 'EXECUTE on function ' || format('%I.%I', namespace.nspname, proc.proname)
	       || '(' || pg_catalog.pg_get_function_identity_arguments(proc.oid) || ')'
	FROM pg_catalog.pg_proc proc
	JOIN pg_catalog.pg_namespace namespace ON namespace.oid = proc.pronamespace
	WHERE (   (namespace.nspname = 'pg_catalog' AND proc.proacl IS NOT NULL)
	       OR (namespace.nspname <> 'information_schema' AND namespace.nspname NOT LIKE 'pg\_%'))
	  AND pg_catalog.has_function_privilege(session_user, proc.oid, 'EXECUTE')
	  AND NOT EXISTS (SELECT 1 FROM declared WHERE pg_catalog.has_function_privilege(declared.rolname, proc.oid, 'EXECUTE'))
	UNION ALL
	SELECT privilege || ' on database ' || quote_ident(current_database())
	FROM unnest(ARRAY['CREATE', 'TEMPORARY']) AS privilege
	WHERE pg_catalog.has_database_privilege(session_user, current_database(), privilege)
	  AND NOT EXISTS (SELECT 1 FROM declared WHERE pg_catalog.has_database_privilege(declared.rolname, current_database(), privilege))
	UNION ALL
	SELECT privilege || ' on schema ' || quote_ident(namespace.nspname)
	FROM pg_catalog.pg_namespace namespace
	CROSS JOIN unnest(ARRAY['CREATE', 'USAGE']) AS privilege
	WHERE namespace.nspname <> 'information_schema'
	  AND namespace.nspname NOT LIKE 'pg\_%'
	  AND pg_catalog.has_schema_privilege(session_user, namespace.oid, privilege)
	  AND NOT EXISTS (SELECT 1 FROM declared WHERE pg_catalog.has_schema_privilege(declared.rolname, namespace.oid, privilege))
	UNION ALL
	SELECT privilege || ' on parameter ' || parameter.parname
	FROM pg_catalog.pg_parameter_acl parameter
	CROSS JOIN unnest(ARRAY['SET', 'ALTER SYSTEM']) AS privilege
	WHERE pg_catalog.has_parameter_privilege(session_user, parameter.parname, privilege)
	  AND NOT EXISTS (SELECT 1 FROM declared WHERE pg_catalog.has_parameter_privilege(declared.rolname, parameter.parname, privilege))
	UNION ALL
	SELECT 'default ' || entry.privilege_type || ' on '
	       || CASE defaults.defaclobjtype
	              WHEN 'r' THEN 'tables'
	              WHEN 'S' THEN 'sequences'
	              WHEN 'f' THEN 'functions'
	              WHEN 'T' THEN 'types'
	              WHEN 'n' THEN 'schemas'
	              ELSE defaults.defaclobjtype::text
	          END
	       || ' created by ' || pg_catalog.pg_get_userbyid(defaults.defaclrole)
	       || coalesce(' in schema ' || quote_ident(namespace.nspname), '')
	FROM pg_catalog.pg_default_acl defaults
	CROSS JOIN login
	CROSS JOIN LATERAL pg_catalog.aclexplode(defaults.defaclacl) AS entry
	LEFT JOIN pg_catalog.pg_namespace namespace ON namespace.oid = defaults.defaclnamespace
	WHERE entry.grantee = login.oid OR defaults.defaclrole = login.oid
	ORDER BY 1`

// reachableRole is one role a login can reach, with everything that would let a
// session holding it leave the tenant boundary.
type reachableRole struct {
	Name         string
	Superuser    bool
	BypassRLS    bool
	CreateRole   bool
	CreateDB     bool
	Replication  bool
	OwnsRelation bool
	OwnsSchema   bool
	OwnsFunction bool
	OwnsDatabase bool
}

// loginAuthority is what a connection's login can reach.
type loginAuthority struct {
	Login       string // session_user
	CurrentRole string // current_user: the role the session starts as
	Reachable   []reachableRole
	// Privileges the login holds that none of the roles its pool declares
	// does, each described as "<privilege> on <object>".
	Privileges []string
}

// loginBoundary is what one pool's login may reach: the roles the store
// declares for it, and nothing that would let a session holding one of them —
// or the bare login — skip or rewrite row-level security.
type loginBoundary struct {
	// Roles are the roles the store declares for the login.
	Roles []string
	// RolesOwnFunctions admits a declared role that owns functions. The
	// migrations make app_control_plane and app_job_worker the owners of the
	// SECURITY DEFINER operations they guard; they make no runtime role the
	// owner of a relation, schema or database, and the login itself the owner
	// of nothing.
	RolesOwnFunctions bool
}

// requestLoginBoundary holds the request login to app_tenant alone.
var requestLoginBoundary = loginBoundary{Roles: []string{tenantDatabaseRole}}

// controlPlaneLoginBoundary holds the control-plane login, which the worker
// pools share, to the roles the store declares for it.
var controlPlaneLoginBoundary = loginBoundary{Roles: controlPlaneLoginRoles, RolesOwnFunctions: true}

type authorityQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// inspectLoginAuthority reads the authority of the login q's connection
// authenticated as: the roles it reaches (loginRolesQuery) and the privileges
// it holds beyond every role boundary declares (loginPrivilegesQuery). Every
// new request, control-plane and worker connection, and the infrastructure
// suite, judge a login from it.
func inspectLoginAuthority(ctx context.Context, q authorityQuerier, boundary loginBoundary) (loginAuthority, error) {
	authority, err := inspectReachableRoles(ctx, q)
	if err != nil {
		return loginAuthority{}, err
	}
	rows, err := q.Query(ctx, loginPrivilegesQuery, boundary.Roles)
	if err != nil {
		return loginAuthority{}, err
	}
	if authority.Privileges, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return loginAuthority{}, err
	}
	return authority, nil
}

// inspectReachableRoles reads the roles the login q's connection authenticated
// as can reach, through loginRolesQuery.
func inspectReachableRoles(ctx context.Context, q authorityQuerier) (loginAuthority, error) {
	rows, err := q.Query(ctx, loginRolesQuery)
	if err != nil {
		return loginAuthority{}, err
	}
	defer rows.Close()
	var authority loginAuthority
	for rows.Next() {
		var role reachableRole
		if err := rows.Scan(&authority.CurrentRole, &authority.Login, &role.Name,
			&role.Superuser, &role.BypassRLS, &role.CreateRole, &role.CreateDB, &role.Replication,
			&role.OwnsRelation, &role.OwnsSchema, &role.OwnsFunction, &role.OwnsDatabase); err != nil {
			return loginAuthority{}, err
		}
		authority.Reachable = append(authority.Reachable, role)
	}
	if err := rows.Err(); err != nil {
		return loginAuthority{}, err
	}
	if len(authority.Reachable) == 0 {
		return loginAuthority{}, errors.New("the session login is not a role in pg_roles")
	}
	return authority, nil
}

// beyond lists everything the login reaches past boundary: one entry per role —
// any role other than the login itself and the declared roles, and any
// reachable role that is a superuser, BYPASSRLS, CREATEROLE, CREATEDB or
// REPLICATION, or that owns a relation, schema or database, or a function
// unless boundary lets that declared role own one — and one per privilege the
// login holds that no declared role does. Empty means a session on this login
// stays inside the policies of the roles it is declared for, whatever role it
// selects.
func (a loginAuthority) beyond(boundary loginBoundary) []string {
	declared := strings.Join(boundary.Roles, ", ")
	if len(boundary.Roles) == 0 {
		// The scoped reader declares no application role, so the only role it
		// may reach is itself.
		declared = "the login itself"
	}
	var excess []string
	for _, role := range a.Reachable {
		isDeclared := role.Name != a.Login && slices.Contains(boundary.Roles, role.Name)
		var reasons []string
		if role.Name != a.Login && !isDeclared {
			reasons = append(reasons, "a role other than "+declared)
		}
		for _, attribute := range []struct {
			held bool
			name string
		}{
			{role.Superuser, "SUPERUSER"},
			{role.BypassRLS, "BYPASSRLS"},
			{role.CreateRole, "CREATEROLE"},
			{role.CreateDB, "CREATEDB"},
			{role.Replication, "REPLICATION"},
			{role.OwnsRelation, "owns a relation"},
			{role.OwnsSchema, "owns a schema"},
			{role.OwnsFunction && !(isDeclared && boundary.RolesOwnFunctions), "owns a function"},
			{role.OwnsDatabase, "owns a database"},
		} {
			if attribute.held {
				reasons = append(reasons, attribute.name)
			}
		}
		if len(reasons) > 0 {
			excess = append(excess, fmt.Sprintf("%s (%s)", role.Name, strings.Join(reasons, ", ")))
		}
	}
	for _, privilege := range a.Privileges {
		if len(boundary.Roles) == 0 {
			// The reader's findings already say how the login came to hold it.
			excess = append(excess, privilege)
			continue
		}
		excess = append(excess, privilege+" (held by the login, not by "+strings.Join(boundary.Roles, " or ")+")")
	}
	return excess
}

// beyondTenant is beyond for the request login: empty means a request session
// on this login stays inside the tenant policies whatever role it selects.
func (a loginAuthority) beyondTenant() []string { return a.beyond(requestLoginBoundary) }

// requireRequestLoginAuthority is the request pool's AfterConnect: it fails
// closed unless the connection's login starts as app_tenant and can reach
// nothing wider. It judges every new physical connection before the pool hands
// it out, not only the first: a membership or privilege granted to the request
// login after startup, or a session default role that did not apply —
// PostgreSQL only warns and leaves the session as the bare login — refuses the
// connection instead of serving from it. The credential is provisioned by the
// deployment, not by this process, so this is where a misprovisioned or
// widened request credential is caught.
func requireRequestLoginAuthority(ctx context.Context, conn *pgx.Conn) error {
	authority, err := inspectLoginAuthority(ctx, conn, requestLoginBoundary)
	if err != nil {
		return fmt.Errorf("inspect request Postgres login authority: %w", err)
	}
	if excess := authority.beyondTenant(); len(excess) > 0 {
		return fmt.Errorf("request Postgres login %q reaches beyond %s: %s; cross-tenant roles belong to the control-plane login and request privileges to %s", authority.Login, tenantDatabaseRole, strings.Join(excess, "; "), tenantDatabaseRole)
	}
	if authority.CurrentRole != tenantDatabaseRole {
		return fmt.Errorf("request Postgres login %q starts as %q, not %s; declare %s as the store's first runtime-read-write role", authority.Login, authority.CurrentRole, tenantDatabaseRole, tenantDatabaseRole)
	}
	return nil
}

// requireControlPlaneLoginAuthority is the AfterConnect of the control-plane
// pool and of the billing, webhook-projection and job worker pools, which all
// authenticate as the control-plane login. It refuses the connection if that
// login reaches any role besides the ones the store declares for it —
// app_control_plane and the three worker roles; if the login or any role it
// reaches is a superuser, BYPASSRLS, CREATEROLE, CREATEDB or REPLICATION role,
// or owns a relation, schema or database; if the login itself owns a function
// (a declared role may: the migrations make app_control_plane and
// app_job_worker the owners of the SECURITY DEFINER operations they guard); or
// if the login holds a privilege none of its declared roles holds. It fails
// closed on any query error. The declared roles span tenants only through the
// exact-role policies and grants the migrations give them, so a login that
// reaches further — a membership, attribute, ownership or direct grant added by
// misprovisioning, before startup or after it — would carry that authority into
// every cross-tenant path. Judging every new connection rather than once at
// boot catches a change made after startup. It records the role the connection
// starts as for returnsToSessionRole.
func requireControlPlaneLoginAuthority(ctx context.Context, conn *pgx.Conn) error {
	authority, err := inspectLoginAuthority(ctx, conn, controlPlaneLoginBoundary)
	if err != nil {
		return fmt.Errorf("inspect control-plane Postgres login authority: %w", err)
	}
	if excess := authority.beyond(controlPlaneLoginBoundary); len(excess) > 0 {
		return fmt.Errorf("control-plane Postgres login %q reaches beyond the roles the store declares for it (%s): %s", authority.Login, strings.Join(controlPlaneLoginRoles, ", "), strings.Join(excess, "; "))
	}
	conn.PgConn().CustomData()[sessionRoleKey] = authority.CurrentRole
	return nil
}

// openScopedBoundary validates this module's transport policy, then delegates
// request-pool ownership, startup checks and credential rotation to service-postgres.
func openScopedBoundary(ctx context.Context, readOnlyConnection, readWriteConnection string, provider scopedpostgres.AccessTokenProvider) (*scopedpostgres.Factory, func(), error) {
	return openScopedBoundaryAs(ctx, readOnlyConnection, readWriteConnection, provider, postgresAuthenticator{})
}

// openScopedBoundaryAs is openScopedBoundary with the authenticator as a
// parameter. It exists so the suite can borrow the library-owned WRITER pool,
// which production never borrows: this module's authenticator refuses
// AuthorizeDatabaseWrite outright, so Factory.Writer returns before it reaches
// the pool and the writer capability's connection policy is never exercised by
// a checkout. A test authenticator that permits writes is the only way to
// reach that path, and it reaches it through THIS function — the same transport
// validation, the same options, the same restricted-session and connection
// policies — so what the suite exercises is the production assembly with one
// substituted decision, not a re-derivation of it.
//
// Production has exactly one caller, openScopedBoundary above, which passes the
// refusing authenticator. Nothing here relaxes that refusal.
func openScopedBoundaryAs(ctx context.Context, readOnlyConnection, readWriteConnection string, provider scopedpostgres.AccessTokenProvider, authenticator scopedpostgres.Authenticator) (*scopedpostgres.Factory, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("scoped Postgres context is required")
	}
	if strings.TrimSpace(readOnlyConnection) == "" || strings.TrimSpace(readWriteConnection) == "" {
		return nil, nil, errors.New("distinct read-only and read-write Postgres connections are required")
	}
	profile, err := DatabaseTransportProfile()
	if err != nil {
		return nil, nil, err
	}
	readerConfig, err := parseDatabaseTransport(readOnlyConnection, profile, provider != nil)
	if err != nil {
		return nil, nil, fmt.Errorf("parse read-only Postgres capability: %w", err)
	}
	writerConfig, err := parseDatabaseTransport(readWriteConnection, profile, provider != nil)
	if err != nil {
		return nil, nil, fmt.Errorf("parse read-write Postgres capability: %w", err)
	}
	if profile == "local-identity-proxy" && readerConfig.ConnConfig.Host == writerConfig.ConnConfig.Host {
		return nil, nil, errors.New("reader and writer require distinct private identity sockets")
	}
	options := []scopedpostgres.Option{
		scopedpostgres.WithScopeSettings("app.current_org_id", "app.current_user_id"),
		scopedpostgres.WithOperationTimeout(scopedBoundaryOperationTimeout),
		// service-postgres judges both scoped pools on every new physical
		// connection and again on every checkout: it refuses a session whose
		// login, or any role it can reach, is a superuser or holds BYPASSRLS,
		// CREATEROLE, CREATEDB or REPLICATION, owns the database, or can reach
		// one of the cross-tenant roles named here. A missing role fails closed.
		// app_tenant is deliberately not denied: the scoped writer authenticates
		// as the request login, which starts as app_tenant.
		scopedpostgres.WithRestrictedSession(controlPlaneLoginRoles...),
		// And this module's own judgement runs on the same two boundaries, so
		// the classes the restricted-session policy does not cover — ownership
		// of a relation, schema or function, excess grants, SECURITY DEFINER
		// grants, sequence and parameter privileges, default ACLs — are judged
		// per connection and per checkout rather than once at startup. Each
		// capability gets the policy its login is actually held to: the reader
		// declares no application role and is judged against what a read-only
		// login may hold, while the scoped writer authenticates as the request
		// login and is judged exactly as the request pool's own connections
		// are. Both run after the library's checks and before the connection
		// serves; a refusal destroys it and fails the acquisition with this
		// error. The pools stay the primitive's: nothing here caches a
		// connection, holds one open, or takes ownership of their lifecycle.
		scopedpostgres.WithConnectionPolicies(readerConnectionPolicy, writerConnectionPolicy),
	}
	if provider != nil {
		options = append(options, scopedpostgres.WithAccessTokenProvider(provider))
	}
	return scopedpostgres.Open(ctx, readOnlyConnection, readWriteConnection, authenticator, options...)
}

// readerConnectionPolicy is the scoped reader's per-connection and per-checkout
// judgement. requireReaderLoginAuthority takes the batching interface so the
// suite can judge a login through a pool as well; a *pgx.Conn satisfies it, and
// this adapter is what makes that a scopedpostgres.ConnectionPolicy.
func readerConnectionPolicy(ctx context.Context, conn *pgx.Conn) error {
	return requireReaderLoginAuthority(ctx, conn)
}

// writerConnectionPolicy judges the scoped writer. That pool authenticates as
// the request login, so it is held to the request login's boundary — the same
// judgement requestPool's own AfterConnect applies. Before the primitive
// exposed this seam the scoped writer was a physically separate pool that no
// application policy ever saw: accounts calls only Reader(), so it carried no
// live traffic, but an unjudged connection on the request credential is a
// surface whether or not anything currently borrows it.
func writerConnectionPolicy(ctx context.Context, conn *pgx.Conn) error {
	return requireRequestLoginAuthority(ctx, conn)
}

// configureConnection parses a Codefly connection secret into a pool config and
// attaches credential rotation. The request, control-plane and worker pools use
// it; the scoped reader/writer pools are opened by service-postgres Open. A nil
// hook preserves the URL-embedded password.
func configureConnection(connectionURL string, hook beforeConnectHook) (*pgxpool.Config, error) {
	if strings.TrimSpace(connectionURL) == "" {
		return nil, errors.New("postgres connection URL is required")
	}
	profile, err := DatabaseTransportProfile()
	if err != nil {
		return nil, err
	}
	config, err := parseDatabaseTransport(connectionURL, profile, hook != nil)
	if err != nil {
		return nil, err
	}
	config.BeforeConnect = hook
	return config, nil
}

// NewPostgresStoreFromURL creates a single-credential store from one explicit
// connection URL, for operator tools and integration tests. It is never the
// served process: NewPostgresStore splits request and cross-tenant work across
// distinct logins, and this constructor deliberately does not.
//
// This path intentionally does NOT attach the token-rotation hook: callers pass
// a fully specified URL (e.g. an operator's admin credential to
// role-catalog-import) and must not have that credential silently replaced by a
// process-wide token file. Token rotation is wired in NewPostgresStore, which
// owns the production pools.
//
// The one credential must hold SET membership in both app_tenant and
// app_control_plane — the migration principal does. A checkout hook selects
// app_tenant on every connection, so an un-wrapped Store call returns zero rows
// from a tenant table when no app.current_org_id is set (fail-closed), WithOrgTx
// adds the transaction-local org, and WithControlPlane assumes app_control_plane
// on the same pool for its transaction. AfterRelease resets the role and every
// session setting before the connection returns to the pool. If app_tenant does
// not exist the checkout fails loudly rather than falling back to the
// credential's own authority.
func NewPostgresStoreFromURL(ctx context.Context, connectionURL string) (*PostgresStore, error) {
	w := wool.Get(ctx).In("NewPostgresStore")

	poolConfig, err := pgxpool.ParseConfig(connectionURL)
	if err != nil {
		return nil, w.Wrapf(err, "failed to parse connection string")
	}
	return newPostgresStoreFromConfig(ctx, poolConfig)
}

// newPostgresStoreFromConfig installs the app_tenant role hooks on an
// already-parsed pool config and opens the single-credential tooling store. The
// caller owns whether a token-rotation BeforeConnect hook is attached.
func newPostgresStoreFromConfig(ctx context.Context, poolConfig *pgxpool.Config) (*PostgresStore, error) {
	w := wool.Get(ctx).In("NewPostgresStore")

	poolConfig.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		// SET ROLE app_tenant — persists for the life of the user's
		// hold on this connection. Tx-scoped role assumptions inside
		// WithControlPlane layer on top and revert on
		// commit/rollback automatically.
		if _, err := conn.Exec(ctx, "SET ROLE app_tenant"); err != nil {
			wool.Get(ctx).In("PrepareConn").Debug("SET ROLE app_tenant failed", wool.ErrField(err))
			return false, nil // destroy this connection; the query retries on a fresh one
		}
		return true, nil
	}
	poolConfig.AfterRelease = func(conn *pgx.Conn) bool {
		// Reset the session before it returns to the pool, in case a caller
		// left a role or a request binding behind; PrepareConn selects
		// app_tenant again on the next checkout. A connection the reset does
		// not clear is dropped, and the pool re-creates it.
		_, clean := resetSession(conn)
		return clean
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, w.Wrapf(err, "failed to connect to database")
	}
	return &PostgresStore{
		Close:        pool.Close,
		pool:         pool,
		controlPlane: pool,
	}, nil
}

// databaseTokenFileEnv names the file an external-identity sidecar rewrites with
// a fresh database access token (e.g. an Entra oss-rdbms token) on a rotation
// interval shorter than the token's ~1h lifetime. External-identity deployments
// set it; local password deployments leave it unset.
const databaseTokenFileEnv = "POSTGRES_TOKEN_FILE"

// databaseTokenFilesEnv maps exact database login names to absolute projected
// token paths. Distinct external identities must not share a single token.
const databaseTokenFilesEnv = "POSTGRES_TOKEN_FILES"

// Database access tokens are compact JWTs. Keep enough headroom for provider
// claim growth while preventing a malformed projected file from allocating
// without bound on every connection attempt.
const maxDatabaseTokenBytes = 64 << 10

// tokenFileAccessTokenProvider selects the exact physical login's projected
// file and rereads it for each connection. Invalid configuration returns a
// refusing provider, never nil: falling back to URL passwords would hide a
// broken external-identity binding. The single-file mode remains available for
// deployments whose principals intentionally use one credential.
func tokenFileAccessTokenProvider() scopedpostgres.AccessTokenProvider {
	path := strings.TrimSpace(os.Getenv(databaseTokenFileEnv))
	if raw := os.Getenv(databaseTokenFilesEnv); raw != "" {
		paths, err := parseTokenFilePaths(raw)
		if path != "" {
			err = errors.New("POSTGRES_TOKEN_FILE and POSTGRES_TOKEN_FILES are mutually exclusive")
		}
		return func(ctx context.Context, username string) (string, error) {
			if err != nil {
				return "", err
			}
			selected, ok := paths[username]
			if !ok {
				return "", fmt.Errorf("no database token file configured for login %q", username)
			}
			return readTokenFile(ctx, selected)
		}
	}
	if path == "" {
		return nil
	}
	return func(ctx context.Context, _ string) (string, error) {
		return readTokenFile(ctx, path)
	}
}

// Reject ambiguous maps (including duplicate keys) without echoing environment
// values into startup logs. Paths are configuration, never credential contents.
func parseTokenFilePaths(raw string) (map[string]string, error) {
	invalid := errors.New("POSTGRES_TOKEN_FILES must be a nonempty JSON object of exact login names to absolute token-file paths")
	d := json.NewDecoder(strings.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, invalid
	}
	paths := make(map[string]string)
	for d.More() {
		key, err := d.Token()
		username, ok := key.(string)
		if err != nil || !ok || username == "" || strings.TrimSpace(username) != username {
			return nil, invalid
		}
		var path string
		if err := d.Decode(&path); err != nil || !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
			return nil, invalid
		}
		if _, exists := paths[username]; exists {
			return nil, invalid
		}
		paths[username] = path
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') || len(paths) == 0 {
		return nil, invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, invalid
	}
	return paths, nil
}

// accessTokenBeforeConnect adapts the shared provider into the pgx hook the
// request and control-plane pools use; neither selects a role on checkout.
func accessTokenBeforeConnect(provider scopedpostgres.AccessTokenProvider) beforeConnectHook {
	if provider == nil {
		return nil
	}
	return func(ctx context.Context, connConfig *pgx.ConnConfig) error {
		token, err := provider(ctx, connConfig.User)
		if err != nil {
			return err
		}
		connConfig.Password = token
		return nil
	}
}

// readTokenFile reads one bounded rotating-token snapshot. os.File reads cannot
// be cancelled portably; spawning a goroutine per connection would only return
// early while leaking blocked readers during a storage failure. The projected
// file is local, so check cancellation before and after the bounded read instead.
// The sidecar contract (platform infrastructure) is to publish each new token with an
// atomic rename; without it a read could observe a partial write, which would
// surface as an authentication failure.
func readTokenFile(ctx context.Context, path string) (string, error) {
	if ctx == nil {
		return "", errors.New("database token read context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read database token file %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxDatabaseTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("read database token file %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(raw) > maxDatabaseTokenBytes {
		return "", fmt.Errorf("database token file %q exceeds %d bytes", path, maxDatabaseTokenBytes)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("database token file %q is empty", path)
	}
	return token, nil
}

var _ business.Store = (*PostgresStore)(nil)

// Pool exposes the underlying pgxpool for callers that need direct access
// (e.g. the pkg/auth/pg package which implements its own interfaces over
// raw SQL). Prefer using the Store methods when possible.
func (s *PostgresStore) Pool() *pgxpool.Pool { return s.pool }

// ProviderRegistered reports whether an identity provider id exists in the
// identity_providers reference catalog. user_identities.provider is a foreign
// key into that catalog, so an unregistered provider cannot create identities;
// startup checks this to fail closed before the first login rather than after.
func (s *PostgresStore) ProviderRegistered(ctx context.Context, providerID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM identity_providers WHERE provider_id = $1)`,
		providerID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("query identity provider registration: %w", err)
	}
	return exists, nil
}

func (s *PostgresStore) RunInTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	// Begin transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}

	// Defer a rollback in case anything fails
	defer func() { _ = tx.Rollback(ctx) }()

	// Create a new context with the transaction. It is on the request pool and
	// binds no request scope, so it runs as app_tenant with nothing set.
	txCtx := txbind.BindRequest(ctx, tx, "", "")

	// Run the provided function
	if err := fn(txCtx); err != nil {
		// If there's an error, rollback and return the error
		return err
	}

	// If everything succeeded, commit the transaction
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	return nil
}

type QueryExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReadQueryExecutor is the repository surface shared by legacy transactions
// and service-postgres ReadTx. It intentionally cannot mutate.
type ReadQueryExecutor interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *PostgresStore) readAs(ctx context.Context, tenantID, userID string, fn func(context.Context, ReadQueryExecutor) error) error {
	if err := auth.RequireVerifiedDatabaseScope(ctx, tenantID, userID); err != nil {
		return err
	}
	if snapshot, ok := ctx.Value(sourceReadSnapshotKey{}).(sourceReadSnapshot); ok {
		return fn(ctx, snapshot.tx)
	}
	if s.database == nil {
		return errors.New("authenticated Postgres boundary is unavailable")
	}
	verifiedTenantID, verifiedUserID, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok {
		return auth.ErrVerifiedDatabaseIdentityRequired
	}
	// service-postgres supports opaque IDs and therefore compares exact strings.
	// Accounts accepts equivalent UUID spellings at its domain boundary, then
	// passes the canonical verified values into the generic capability checks.
	if err := s.database.RequireTenant(ctx, verifiedTenantID); err != nil {
		return err
	}
	if err := s.database.RequireUser(ctx, verifiedUserID); err != nil {
		return err
	}
	reader, err := s.database.Reader(ctx)
	if err != nil {
		return err
	}
	return reader.InTransaction(ctx, func(ctx context.Context, tx scopedpostgres.ReadTx) error {
		return fn(ctx, tx)
	})
}

// getQueryExecutor returns the store transaction ctx carries, whatever pool it
// was opened on — Store methods run inside WithControlPlane on the control-plane
// transaction, as they run inside WithOrgTx on the request one — and the request
// pool when ctx carries none.
func (s *PostgresStore) getQueryExecutor(ctx context.Context) QueryExecutor {
	if tx := storetx.Tx(ctx); tx != nil {
		return tx
	}
	return s.pool
}

func (s *PostgresStore) GetUserByIdentity(ctx context.Context, identity *gen.UserIdentity) (*gen.User, error) {
	w := wool.Get(ctx).In("GetUserByIdentity")
	executor := s.getQueryExecutor(ctx)

	var user gen.User
	query := `
        SELECT u.uuid, u.primary_email, u.created_at, u.updated_at, u.last_login, 
               u.status, u.profile, u.email_verified
        FROM users u
        JOIN user_identities ui ON u.uuid = ui.user_uuid
        WHERE ui.provider = $1 AND ui.provider_id = $2`

	var (
		createdAt time.Time
		updatedAt time.Time
		lastLogin *time.Time
		profile   []byte // for JSONB
		status    string
	)

	err := executor.QueryRow(ctx, query, identity.Provider, identity.ProviderId).Scan(
		&user.Uuid,
		&user.PrimaryEmail,
		&createdAt,
		&updatedAt,
		&lastLogin,
		&status,
		&profile,
		&user.EmailVerified,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, w.Wrapf(err, "failed to scan user")
	}

	// Convert timestamps to protobuf
	user.CreatedAt = timestamppb.New(createdAt)
	user.UpdatedAt = timestamppb.New(updatedAt)
	if lastLogin != nil {
		user.LastLogin = timestamppb.New(*lastLogin)
	}

	// Parse status
	user.Status = parseUserStatus(status)

	// Parse profile JSONB
	if len(profile) > 0 {
		profileMap := make(map[string]string)
		if err := json.Unmarshal(profile, &profileMap); err != nil {
			return nil, w.Wrapf(err, "failed to unmarshal profile")
		}
		user.Profile = profileMap
	}

	return &user, nil
}
func (s *PostgresStore) RegisterUser(ctx context.Context, user *gen.User, identity *gen.UserIdentity) error {
	w := wool.Get(ctx).In("RegisterUser")

	return pgx.BeginTxFunc(ctx, s.controlPlane, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	}, func(tx pgx.Tx) error {
		// Registration is pre-auth (no user/org context yet) and writes
		// users + user_identities + the personal org — all RLS-protected.
		// Registration is an explicit control-plane capability because no
		// tenant or user scope exists until these rows have been created, so it
		// runs on the control-plane pool, whose login alone holds the role.
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+controlPlaneDatabaseRole); err != nil {
			return w.Wrapf(err, "assume control-plane role for registration")
		}
		ctx = txbind.BindControlPlane(ctx, tx)
		executor := s.getQueryExecutor(ctx)

		// First check if this identity already exists
		var existingUserUUID string
		err := executor.QueryRow(ctx, `
            SELECT user_uuid 
            FROM user_identities 
            WHERE provider = $1 AND provider_id = $2`,
			identity.Provider,
			identity.ProviderId,
		).Scan(&existingUserUUID)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return w.Wrapf(err, "failed to check existing identity")
		}

		// If identity exists, return AlreadyExists error
		if existingUserUUID != "" {
			return status.Errorf(codes.AlreadyExists,
				"user already exists with provider %s and id %s",
				identity.Provider, identity.ProviderId)
		}

		// If it's a new identity, check if email is already registered
		var existingEmailUserUUID string
		err = executor.QueryRow(ctx, `
            SELECT uuid 
            FROM users 
            WHERE primary_email = $1`,
			user.PrimaryEmail,
		).Scan(&existingEmailUserUUID)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return w.Wrapf(err, "failed to check existing email")
		}

		// If email exists, might want to handle linking instead of error
		if existingEmailUserUUID != "" {
			return status.Errorf(codes.AlreadyExists,
				"email %s is already registered",
				user.PrimaryEmail)
		}

		// The uuid is caller-supplied, so the insert below can collide on the
		// primary key. Deleted users keep their row, so this deliberately does
		// not filter on status.
		var uuidTaken bool
		err = executor.QueryRow(ctx, `
            SELECT EXISTS (SELECT 1 FROM users WHERE uuid = $1)`,
			user.Uuid,
		).Scan(&uuidTaken)
		if err != nil {
			return w.Wrapf(err, "failed to check existing user id")
		}
		if uuidTaken {
			return status.Errorf(codes.AlreadyExists,
				"user id %s is already registered",
				user.Uuid)
		}

		// Create new user
		profileJSON, err := json.Marshal(user.Profile)
		if err != nil {
			return w.Wrapf(err, "failed to marshal profile")
		}

		_, err = executor.Exec(ctx, `
            INSERT INTO users (
                uuid, primary_email, created_at, updated_at, status,
                profile, email_verified
            ) VALUES (
                $1, $2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, $3,
                $4, $5
            )`,
			user.Uuid,
			user.PrimaryEmail,
			userStatusToString(user.Status),
			profileJSON,
			identity.EmailVerified, // Use identity's email verification status
		)
		if err != nil {
			return w.Wrapf(err, "failed to insert user")
		}

		// Create the identity
		providerDataJSON, err := json.Marshal(identity.ProviderData)
		if err != nil {
			return w.Wrapf(err, "failed to marshal provider data")
		}

		_, err = executor.Exec(ctx, `
            INSERT INTO user_identities (
                uuid, user_uuid, provider, provider_id, provider_email,
                created_at, provider_data, email_verified
            ) VALUES (
                $1, $2, $3, $4, $5,
                CURRENT_TIMESTAMP, $6, $7
            )`,
			identity.Uuid,
			user.Uuid,
			identity.Provider,
			identity.ProviderId,
			identity.ProviderEmail,
			providerDataJSON,
			identity.EmailVerified,
		)
		if err != nil {
			return w.Wrapf(err, "failed to insert identity")
		}

		return nil
	})
}

func (s *PostgresStore) LinkIdentity(ctx context.Context, userUUID string, identity *gen.UserIdentity) error {
	w := wool.Get(ctx).In("LinkIdentity")

	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	}, func(tx pgx.Tx) error {
		// A fresh request connection carries no request scope.
		ctx = txbind.BindRequest(ctx, tx, "", "")
		executor := s.getQueryExecutor(ctx)

		// Check if identity already exists
		var existingUserUUID string
		err := executor.QueryRow(ctx, `
            SELECT user_uuid 
            FROM user_identities 
            WHERE provider = $1 AND provider_id = $2`,
			identity.Provider,
			identity.ProviderId,
		).Scan(&existingUserUUID)

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return w.Wrapf(err, "failed to check existing identity")
		}

		if existingUserUUID != "" {
			return status.Errorf(codes.AlreadyExists,
				"identity already exists with provider %s and id %s",
				identity.Provider, identity.ProviderId)
		}

		// Create the new identity
		providerDataJSON, err := json.Marshal(identity.ProviderData)
		if err != nil {
			return w.Wrapf(err, "failed to marshal provider data")
		}

		_, err = executor.Exec(ctx, `
            INSERT INTO user_identities (
                uuid, user_uuid, provider, provider_id, provider_email,
                created_at, provider_data, email_verified
            ) VALUES (
                $1, $2, $3, $4, $5,
                CURRENT_TIMESTAMP, $6, $7
            )`,
			identity.Uuid,
			userUUID,
			identity.Provider,
			identity.ProviderId,
			identity.ProviderEmail,
			providerDataJSON,
			identity.EmailVerified,
		)
		if err != nil {
			return w.Wrapf(err, "failed to insert identity")
		}

		return nil
	})
}

func (s *PostgresStore) ClearAll(ctx context.Context) error {
	w := wool.Get(ctx).In("ClearAll")

	// Most tables here are RLS-protected (Phase 2B-2F). A bare DELETE
	// under app_tenant with no app.current_org_id set returns ZERO
	// rows — that's fail-closed for production but sabotages test
	// cleanup. Wrap in WithControlPlane so the deletes actually fire across
	// every tenant.
	//
	// Use DELETE instead of TRUNCATE to avoid CASCADE wiping roles
	// table. Errors are intentionally swallowed per-statement:
	// ClearAll runs in test cleanup against a possibly partial
	// schema, and we want best-effort even if some tables haven't
	// been migrated yet.
	return s.WithControlPlane(ctx, func(ctx context.Context) error {
		executor := s.getQueryExecutor(ctx)
		for _, stmt := range []string{
			// gdpr_requests deliberately does not reference users: a privacy
			// request outlives its subject, so nothing cascades it away.
			"DELETE FROM gdpr_requests",
			"DELETE FROM role_assignments",
			"DELETE FROM role_permissions WHERE role_id IN (SELECT id FROM roles WHERE NOT built_in)",
			"DELETE FROM roles WHERE NOT built_in",
			"DELETE FROM team_members",
			"DELETE FROM teams",
			"DELETE FROM organization_members",
			"DELETE FROM organizations",
			"DELETE FROM user_identities",
			"DELETE FROM users",
		} {
			if _, err := executor.Exec(ctx, stmt); err != nil {
				w.Debug("ClearAll statement failed (continuing)",
					wool.Field("stmt", stmt), wool.ErrField(err))
			}
		}
		return nil
	})
}

// Helper functions for status conversion
func parseUserStatus(status string) gen.UserStatus {
	switch status {
	case "active":
		return gen.UserStatus_USER_STATUS_ACTIVE
	case "inactive":
		return gen.UserStatus_USER_STATUS_INACTIVE
	case "suspended":
		return gen.UserStatus_USER_STATUS_SUSPENDED
	case "deleted":
		return gen.UserStatus_USER_STATUS_DELETED
	default:
		return gen.UserStatus_USER_STATUS_UNSPECIFIED
	}
}

func userStatusToString(status gen.UserStatus) string {
	switch status {
	case gen.UserStatus_USER_STATUS_ACTIVE:
		return "active"
	case gen.UserStatus_USER_STATUS_INACTIVE:
		return "inactive"
	case gen.UserStatus_USER_STATUS_SUSPENDED:
		return "suspended"
	case gen.UserStatus_USER_STATUS_DELETED:
		return "deleted"
	default:
		return "active" // Default to active for new users
	}
}
