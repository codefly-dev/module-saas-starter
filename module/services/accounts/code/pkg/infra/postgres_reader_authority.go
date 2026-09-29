package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The scoped reader is the one pool whose login is not held to an application
// role. The Postgres agent provisions it with SELECT grants of its own rather
// than membership in app_tenant, so the "privilege no declared role holds"
// comparison the request and control-plane logins are judged by
// (loginPrivilegesQuery) says nothing here: with no declared role, every
// privilege the reader holds would read as excess, PUBLIC's included.
//
// A view that is not security_invoker executes as its owner, so it is the same
// authority-crossing shape as a SECURITY DEFINER function — but views exist on
// the provisioned baseline and refusing them would refuse a correct deployment,
// so they are not judged here. The three that crossed tenants are closed where
// they live, by 14_delegation_views_security_invoker.up.sql; a new
// non-security_invoker view added later would cross the same way and pass this
// judgement, so that property belongs with whoever adds a view.
// DATABASE_AUTHORITY.md records the crossing, its scope and what remains open.
//
// A reader is judged against what a read-only login legitimately holds instead.
// On the provisioned baseline that is: CONNECT on the database, USAGE on
// schema public, SELECT on the application relations, and a default privilege
// granting it SELECT on tables created later. Everything else it appears to
// hold — EXECUTE on ~600 ordinary functions, SELECT on the readable system
// catalog, UPDATE on pg_catalog.pg_settings — is granted to PUBLIC, so every
// role in the cluster holds it, app_tenant included; none of it is authority
// the reader has over any other role, and refusing it would refuse a correctly
// provisioned reader at startup.
//
// What the judgement therefore looks for is authority the reader holds that a
// read-only login must not: a role it can become, an attribute or ownership
// that skips row-level security, a write, or a grant made to the login itself.

// readOnlyLoginBoundary holds the reader to itself: it declares no application
// role, so any reachable role at all is one too many, and — RolesOwnFunctions
// being false — the login owning a function is refused like any other ownership.
var readOnlyLoginBoundary = loginBoundary{}

// readOnlyLoginPrivilegesQuery returns every privilege the reader's login holds
// that a read-only login must not, one row per finding. Empty means the login
// can only read, and only what it was granted to read.
//
// The branches, and why each is judged the way it is:
//
//   - A write on an application relation — INSERT, UPDATE, DELETE, TRUNCATE,
//     REFERENCES or TRIGGER — is judged by any path, PUBLIC included, because a
//     read-only login holds none on the baseline and a write granted to PUBLIC
//     reaches the reader as surely as one granted to it directly. The system
//     catalog is excluded here: PUBLIC holds UPDATE on pg_catalog.pg_settings
//     by default, which is how any session issues SET, and a grant on the
//     catalog is caught by the "granted to the login" branches below instead.
//
//   - Any sequence privilege is judged by any path. USAGE and UPDATE advance a
//     sequence, SELECT reads its value, and the provisioned reader holds none
//     of the three, so a read-only login needs no sequence privilege at all.
//
//   - EXECUTE on a SECURITY DEFINER function is judged by any path. Such a
//     function runs as its owner, so executing one lends the reader that
//     owner's authority — on this store 22 of the 30 are owned by
//     app_control_plane, whose policies span every tenant. The baseline revokes
//     PUBLIC's EXECUTE on all of them and grants the reader none, so the reader
//     can execute none; judging by any path catches both a grant to the login
//     and a grant to PUBLIC. An ordinary function is not judged here: it runs
//     as the caller, so a grant of it to PUBLIC gives this login nothing it does
//     not share with every role in the cluster, which is why the ~600 PUBLIC may
//     execute are not excess. That is a statement about who holds the grant, not
//     a claim that every such function is harmless: a C-language or extension
//     function may do more than its arguments suggest. A grant made to the login
//     itself is judged whatever the function is.
//
//   - EXECUTE granted to the login itself on any function, ordinary or not,
//     application schema or catalog, is excess: the baseline grants the reader
//     no function, so such a grant is something added to this login alone.
//
//   - A relation or column privilege granted to the login itself is excess
//     unless it is SELECT on an application object, which is exactly what the
//     agent provisions. On the system catalog even SELECT is excess: a grant of
//     SELECT on pg_authid reads every role's password hash, and the catalog a
//     reader legitimately reads it reads through PUBLIC, never through a grant
//     of its own.
//
//   - CREATE or TEMPORARY on the database, and CREATE on an application schema,
//     are judged by any path. Either lets the login create a relation, without
//     row-level security, that an unqualified name in another session's
//     search_path can resolve to — pg_temp is searched first. The baseline
//     revokes both from PUBLIC, so neither reaches a correctly provisioned
//     reader. CONNECT is not judged: without it the login cannot open the
//     connection being judged. USAGE on a schema is not judged: without USAGE on
//     public the reader's own SELECT grants are unusable.
//
//   - Any parameter privilege is judged by any path: SET on
//     session_replication_role switches off every trigger, and ALTER SYSTEM on
//     archive_command has the server run a command. Only parameters something
//     has granted on appear in pg_parameter_acl, and the baseline has none.
//
//   - A default privilege is excess unless it is SELECT on tables, which is the
//     one the agent provisions so later migrations keep the reader readable. A
//     default ACL defined for objects the login itself creates is excess
//     outright: a read-only login creates nothing.
//
// Not judged, for the same reasons DATABASE_AUTHORITY.md gives for the request
// login: USAGE on a type, domain, language, foreign-data wrapper or foreign
// server, CREATE on a tablespace, and large-object privileges. None reads or
// writes a tenant row on its own. A privilege granted to PUBLIC on an ordinary
// object is likewise not excess for the reader, because it is held by every
// role in the cluster rather than by this login: a deployment that grants
// PUBLIC a write or a definer function is caught by the by-any-path branches
// above, and one that grants PUBLIC something else has widened every role at
// once, which is the store's baseline to answer for and not this pool's.
const readOnlyLoginPrivilegesQuery = `
	WITH RECURSIVE reachable AS (
		SELECT oid FROM pg_catalog.pg_roles WHERE rolname = session_user
		UNION
		SELECT members.roleid FROM pg_catalog.pg_auth_members members
		JOIN reachable ON members.member = reachable.oid
	), application AS (
		SELECT rel.oid, rel.relkind, format('%I.%I', namespace.nspname, rel.relname) AS name
		FROM pg_catalog.pg_class rel
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid = rel.relnamespace
		WHERE rel.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
		  AND namespace.nspname <> 'information_schema'
		  AND namespace.nspname NOT LIKE 'pg\_%'
	)
	SELECT privilege || ' on table ' || application.name
	FROM application
	CROSS JOIN unnest(ARRAY['INSERT', 'UPDATE', 'DELETE', 'TRUNCATE', 'REFERENCES', 'TRIGGER']) AS privilege
	WHERE application.relkind <> 'S'
	  AND pg_catalog.has_table_privilege(session_user, application.oid, privilege)
	UNION ALL
	SELECT privilege || ' on sequence ' || application.name
	FROM application
	CROSS JOIN unnest(ARRAY['USAGE', 'SELECT', 'UPDATE']) AS privilege
	WHERE application.relkind = 'S'
	  AND pg_catalog.has_sequence_privilege(session_user, application.oid, privilege)
	UNION ALL
	SELECT 'EXECUTE on SECURITY DEFINER function '
	       || format('%I.%I', namespace.nspname, proc.proname)
	       || '(' || pg_catalog.pg_get_function_identity_arguments(proc.oid) || ')'
	       || ', which runs as ' || pg_catalog.pg_get_userbyid(proc.proowner)
	FROM pg_catalog.pg_proc proc
	JOIN pg_catalog.pg_namespace namespace ON namespace.oid = proc.pronamespace
	WHERE proc.prosecdef
	  AND namespace.nspname <> 'information_schema'
	  AND namespace.nspname NOT LIKE 'pg\_%'
	  AND pg_catalog.has_function_privilege(session_user, proc.oid, 'EXECUTE')
	UNION ALL
	SELECT 'EXECUTE granted to the login on function '
	       || format('%I.%I', namespace.nspname, proc.proname)
	       || '(' || pg_catalog.pg_get_function_identity_arguments(proc.oid) || ')'
	FROM pg_catalog.pg_proc proc
	JOIN pg_catalog.pg_namespace namespace ON namespace.oid = proc.pronamespace
	CROSS JOIN LATERAL pg_catalog.aclexplode(proc.proacl) AS entry
	WHERE entry.grantee IN (SELECT oid FROM reachable)
	UNION ALL
	SELECT entry.privilege_type || ' granted to the login on table ' || format('%I.%I', namespace.nspname, rel.relname)
	FROM pg_catalog.pg_class rel
	JOIN pg_catalog.pg_namespace namespace ON namespace.oid = rel.relnamespace
	CROSS JOIN LATERAL pg_catalog.aclexplode(rel.relacl) AS entry
	WHERE entry.grantee IN (SELECT oid FROM reachable)
	  AND (entry.privilege_type <> 'SELECT' OR namespace.nspname = 'pg_catalog')
	UNION ALL
	SELECT entry.privilege_type || ' granted to the login on column '
	       || format('%I.%I.%I', namespace.nspname, rel.relname, attribute.attname)
	FROM pg_catalog.pg_attribute attribute
	JOIN pg_catalog.pg_class rel ON rel.oid = attribute.attrelid
	JOIN pg_catalog.pg_namespace namespace ON namespace.oid = rel.relnamespace
	CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS entry
	WHERE entry.grantee IN (SELECT oid FROM reachable)
	  AND (entry.privilege_type <> 'SELECT' OR namespace.nspname = 'pg_catalog')
	UNION ALL
	SELECT privilege || ' on database ' || quote_ident(current_database())
	FROM unnest(ARRAY['CREATE', 'TEMPORARY']) AS privilege
	WHERE pg_catalog.has_database_privilege(session_user, current_database(), privilege)
	UNION ALL
	SELECT 'CREATE on schema ' || quote_ident(namespace.nspname)
	FROM pg_catalog.pg_namespace namespace
	WHERE namespace.nspname <> 'information_schema'
	  AND namespace.nspname NOT LIKE 'pg\_%'
	  AND pg_catalog.has_schema_privilege(session_user, namespace.oid, 'CREATE')
	UNION ALL
	SELECT privilege || ' on parameter ' || parameter.parname
	FROM pg_catalog.pg_parameter_acl parameter
	CROSS JOIN unnest(ARRAY['SET', 'ALTER SYSTEM']) AS privilege
	WHERE pg_catalog.has_parameter_privilege(session_user, parameter.parname, privilege)
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
	FROM pg_catalog.pg_default_acl defaults
	CROSS JOIN LATERAL pg_catalog.aclexplode(defaults.defaclacl) AS entry
	WHERE (entry.grantee IN (SELECT oid FROM reachable)
	       AND NOT (entry.privilege_type = 'SELECT' AND defaults.defaclobjtype = 'r'))
	   OR defaults.defaclrole IN (SELECT oid FROM reachable)
	ORDER BY 1`

// authorityBatcher is the connection a reader judgement runs on. Both *pgx.Conn
// — what the pool hands its hooks — and *pgxpool.Pool satisfy it, so the suite
// can judge a login without reaching into a pool's connections.
type authorityBatcher interface {
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}

// inspectReaderAuthority reads the reader login's authority in one round trip:
// the roles it reaches with their attributes and ownership (loginRolesQuery),
// and the privileges a read-only login must not hold
// (readOnlyLoginPrivilegesQuery). They share a batch rather than paying two
// round trips, so that the judgement costs one round trip wherever it runs —
// today at startup, and on every connection and checkout once the primitive
// exposes the seam requireReaderLoginAuthority's comment describes.
func inspectReaderAuthority(ctx context.Context, conn authorityBatcher) (loginAuthority, error) {
	batch := &pgx.Batch{}
	batch.Queue(loginRolesQuery)
	batch.Queue(readOnlyLoginPrivilegesQuery)
	results := conn.SendBatch(ctx, batch)
	defer results.Close() //nolint:errcheck // the error surfaces from the reads below

	var authority loginAuthority
	roles, err := results.Query()
	if err != nil {
		return loginAuthority{}, err
	}
	for roles.Next() {
		var role reachableRole
		if err := roles.Scan(&authority.CurrentRole, &authority.Login, &role.Name,
			&role.Superuser, &role.BypassRLS, &role.CreateRole, &role.CreateDB, &role.Replication,
			&role.OwnsRelation, &role.OwnsSchema, &role.OwnsFunction, &role.OwnsDatabase); err != nil {
			return loginAuthority{}, err
		}
		authority.Reachable = append(authority.Reachable, role)
	}
	if err := roles.Err(); err != nil {
		return loginAuthority{}, err
	}
	if len(authority.Reachable) == 0 {
		return loginAuthority{}, errors.New("the session login is not a role in pg_roles")
	}

	privileges, err := results.Query()
	if err != nil {
		return loginAuthority{}, err
	}
	if authority.Privileges, err = pgx.CollectRows(privileges, pgx.RowTo[string]); err != nil {
		return loginAuthority{}, err
	}
	return authority, results.Close()
}

// beyondReadOnly is beyond for the scoped reader: empty means a session on this
// login can read what it was granted to read and do nothing else.
func (a loginAuthority) beyondReadOnly() []string { return a.beyond(readOnlyLoginBoundary) }

// reportedExcess bounds how many findings a refusal names. One widening can
// produce a finding per object: granting the reader a role that holds the
// application's own grants yields several hundred, which would make the startup
// error unreadable and bury the reason it begins with. The role and attribute
// findings beyond() produces come first, so the named ones are the ones that
// say what went wrong.
const reportedExcess = 12

// summarizeExcess renders at most reportedExcess findings and counts the rest.
func summarizeExcess(excess []string) string {
	if len(excess) <= reportedExcess {
		return strings.Join(excess, "; ")
	}
	return fmt.Sprintf("%s; and %d more", strings.Join(excess[:reportedExcess], "; "), len(excess)-reportedExcess)
}

// requireReaderLoginAuthority judges a scoped reader connection. It fails closed
// on any query error, and refuses a login that reaches any role at all, holds an
// attribute or ownership that skips row-level security, or holds a privilege a
// read-only login must not.
//
// It runs ONCE, at startup, from verifyReaderLogin — on a connection of the
// reader's own capability, but not on the pooled connections that go on to
// serve. service-postgres owns the scoped reader and writer pools and the
// version pinned here exposes no hook for an application policy, so a
// membership or grant made after startup is not caught on those pools: what
// runs there per connection and per checkout is the library's own
// restricted-session policy, which covers privileged attributes, database
// ownership and the denied cross-tenant roles, and nothing else. Ownership of a
// relation, schema or function and excess or SECURITY DEFINER grants are
// therefore judged at boot only. DATABASE_AUTHORITY.md records the layering and
// the seam that closes it; move this judgement onto that seam when the
// primitive publishes it, and correct this comment and that section together.
//
// Neither layer is a fence against a GRANT that commits while a borrower
// already holds the connection; a role or grant change still needs the operator
// drain policy DATABASE_AUTHORITY.md describes.
func requireReaderLoginAuthority(ctx context.Context, conn authorityBatcher) error {
	authority, err := inspectReaderAuthority(ctx, conn)
	if err != nil {
		return fmt.Errorf("inspect read-only Postgres login authority: %w", err)
	}
	if excess := authority.beyondReadOnly(); len(excess) > 0 {
		return fmt.Errorf("read-only Postgres login %q reaches beyond a read-only capability: %s; the scoped reader may only read what it was granted to read", authority.Login, summarizeExcess(excess))
	}
	if authority.CurrentRole != authority.Login {
		return fmt.Errorf("read-only Postgres login %q starts as %q; the scoped reader selects no role and the store declares none for it", authority.Login, authority.CurrentRole)
	}
	return nil
}
