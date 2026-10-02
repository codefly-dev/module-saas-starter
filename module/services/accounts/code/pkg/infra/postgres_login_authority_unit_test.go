package infra

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Every request connection and the infrastructure suite judge the request login
// by the same rule. Each attribute or ownership that would let a request session
// skip or rewrite RLS is reported on its own, for the login itself and for
// anything it reaches, and so is any reachable role other than app_tenant and
// any privilege loginPrivilegesQuery finds the login holding that app_tenant
// does not.
func TestBeyondTenantReportsEveryWayOut(t *testing.T) {
	tenantBound := loginAuthority{
		Login:       "request_login",
		CurrentRole: tenantDatabaseRole,
		Reachable:   []reachableRole{{Name: "request_login"}, {Name: tenantDatabaseRole}},
	}
	require.Empty(t, tenantBound.beyondTenant())

	for name, tc := range map[string]struct {
		mutate func(*reachableRole)
		want   string
	}{
		"superuser":          {func(r *reachableRole) { r.Superuser = true }, "SUPERUSER"},
		"bypassrls":          {func(r *reachableRole) { r.BypassRLS = true }, "BYPASSRLS"},
		"createrole":         {func(r *reachableRole) { r.CreateRole = true }, "CREATEROLE"},
		"createdb":           {func(r *reachableRole) { r.CreateDB = true }, "CREATEDB"},
		"replication":        {func(r *reachableRole) { r.Replication = true }, "REPLICATION"},
		"relation owner":     {func(r *reachableRole) { r.OwnsRelation = true }, "owns a relation"},
		"schema owner":       {func(r *reachableRole) { r.OwnsSchema = true }, "owns a schema"},
		"function owner":     {func(r *reachableRole) { r.OwnsFunction = true }, "owns a function"},
		"database owner":     {func(r *reachableRole) { r.OwnsDatabase = true }, "owns a database"},
		"other role reached": {func(r *reachableRole) { r.Name = controlPlaneDatabaseRole }, "a role other than app_tenant"},
	} {
		t.Run(name, func(t *testing.T) {
			for index := range tenantBound.Reachable {
				authority := loginAuthority{Login: tenantBound.Login, CurrentRole: tenantBound.CurrentRole}
				authority.Reachable = append([]reachableRole(nil), tenantBound.Reachable...)
				tc.mutate(&authority.Reachable[index])
				excess := authority.beyondTenant()
				require.Len(t, excess, 1)
				require.Contains(t, excess[0], tc.want)
			}
		})
	}

	withPrivilege := tenantBound
	withPrivilege.Privileges = []string{"INSERT on table public.platform_admins"}
	excess := withPrivilege.beyondTenant()
	require.Len(t, excess, 1)
	require.Contains(t, excess[0], "INSERT on table public.platform_admins")
	require.Contains(t, excess[0], "not by app_tenant")
}

// The control-plane login, which the worker pools share, is judged by the same
// rule against the roles the store declares for it. Those roles may own the
// functions the migrations give them; the login may own nothing, and no role it
// reaches may own a relation, schema or database or carry an attribute that
// skips or rewrites RLS.
func TestBeyondControlPlaneReportsEveryWayOut(t *testing.T) {
	declared := loginAuthority{
		Login: "control_login",
		Reachable: []reachableRole{
			{Name: "control_login"},
			{Name: controlPlaneDatabaseRole, OwnsFunction: true},
			{Name: billingWorkerDatabaseRole},
			{Name: webhookProjectionDatabaseRole},
			{Name: jobWorkerDatabaseRole, OwnsFunction: true},
		},
	}
	require.Empty(t, declared.beyond(controlPlaneLoginBoundary))

	for name, tc := range map[string]struct {
		index  int
		mutate func(*reachableRole)
		want   string
	}{
		"undeclared role reached": {1, func(r *reachableRole) { r.Name = tenantDatabaseRole }, "a role other than app_control_plane"},
		"bypassrls role":          {2, func(r *reachableRole) { r.BypassRLS = true }, "BYPASSRLS"},
		"createdb login":          {0, func(r *reachableRole) { r.CreateDB = true }, "CREATEDB"},
		"superuser role":          {3, func(r *reachableRole) { r.Superuser = true }, "SUPERUSER"},
		"createrole role":         {4, func(r *reachableRole) { r.CreateRole = true }, "CREATEROLE"},
		"replication login":       {0, func(r *reachableRole) { r.Replication = true }, "REPLICATION"},
		"relation owner":          {1, func(r *reachableRole) { r.OwnsRelation = true }, "owns a relation"},
		"schema owner":            {4, func(r *reachableRole) { r.OwnsSchema = true }, "owns a schema"},
		"database owner":          {0, func(r *reachableRole) { r.OwnsDatabase = true }, "owns a database"},
		"function-owning login":   {0, func(r *reachableRole) { r.OwnsFunction = true }, "owns a function"},
	} {
		t.Run(name, func(t *testing.T) {
			authority := loginAuthority{Login: declared.Login}
			authority.Reachable = append([]reachableRole(nil), declared.Reachable...)
			tc.mutate(&authority.Reachable[tc.index])
			excess := authority.beyond(controlPlaneLoginBoundary)
			require.Len(t, excess, 1)
			require.Contains(t, excess[0], tc.want)
		})
	}

	// A function owned by an undeclared role is named for the role, not
	// admitted because declared roles may own functions.
	undeclaredOwner := declared
	undeclaredOwner.Reachable = append(append([]reachableRole(nil), declared.Reachable...), reachableRole{Name: "migration_owner", OwnsFunction: true})
	excess := undeclaredOwner.beyond(controlPlaneLoginBoundary)
	require.Len(t, excess, 1)
	require.Contains(t, excess[0], "migration_owner")
	require.Contains(t, excess[0], "owns a function")

	withPrivilege := declared
	withPrivilege.Privileges = []string{"SELECT on table public.platform_admins"}
	excess = withPrivilege.beyond(controlPlaneLoginBoundary)
	require.Len(t, excess, 1)
	require.Contains(t, excess[0], "SELECT on table public.platform_admins")
	require.Contains(t, excess[0], "not by app_control_plane or app_billing_worker or app_webhook_worker or app_job_worker")
}

// The control-plane capability must be a login of its own: sharing one with the
// reader is refused as firmly as sharing one with the request login.
func TestControlPlaneLoginMustBeDistinctFromReaderAndRequest(t *testing.T) {
	clearDatabaseEnvironment(t)
	config := func(user string) *pgxpool.Config {
		parsed, err := pgxpool.ParseConfig("postgres://" + user + ":secret@127.0.0.1:5432/users")
		require.NoError(t, err)
		return parsed
	}
	reader, request, control := config("reader"), config("writer"), config("control")
	require.NoError(t, requireDistinctControlPlaneLogin(reader, request, control))
	require.ErrorContains(t, requireDistinctControlPlaneLogin(reader, request, config("writer")),
		"request and control-plane Postgres capabilities must use distinct logins")
	require.ErrorContains(t, requireDistinctControlPlaneLogin(reader, request, config("reader")),
		"read-only and control-plane Postgres capabilities must use distinct logins")
}
