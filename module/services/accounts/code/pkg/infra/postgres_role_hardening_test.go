package infra_test

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type relationPrivileges struct {
	selectRows bool
	insertRows bool
	updateRows bool
	deleteRows bool
}

type relationScope string

const (
	relationScopeGlobal  relationScope = "global"
	relationScopeTenant  relationScope = "tenant"
	relationScopeUser    relationScope = "user"
	relationScopePreAuth relationScope = "pre_auth"
	relationScopeJob     relationScope = "job"
	relationScopeWorker  relationScope = "worker"
)

var relationsByScope = map[relationScope][]string{
	relationScopeGlobal: {
		"audit_event_types",
		"bootstrap_state",
		"data_retention_policies",
		"email_templates",
		"feature_flags",
		"identity_providers",
		"plan_entitlements",
		"plans",
		"platform_admins",
	},
	relationScopeTenant: {
		"actor_chain_journal",
		"actor_chain_revocations",
		"api_keys",
		"approval_decisions",
		"approval_requests",
		"audit_events",
		"connector_credentials",
		"delegation_grants",
		"entitlement_overrides",
		"invitations",
		"org_identity_providers",
		"org_settings",
		"organization_activations",
		"organization_authorization_revisions",
		"organization_members",
		"organizations",
		"principal_authorization_revisions",
		"principals",
		"record_shares",
		"role_assignments",
		"role_permissions",
		"roles",
		"scope_grants",
		"scope_nodes",
		"subscriptions",
		"team_members",
		"teams",
		"usage_events",
		"usage_totals",
		"webhook_deliveries",
		"webhook_subscriptions",
	},
	relationScopeUser: {
		"gdpr_requests",
		"mfa_backup_codes",
		"mfa_devices",
		"mfa_login_transactions",
		"notifications",
		"onboarding_progress",
		"sessions",
		"user_consent_events",
		"user_consent_preferences",
		"user_identities",
		"users",
		"webauthn_ceremonies",
		"webauthn_credentials",
	},
	relationScopePreAuth: {"magic_links", "waitlist_entries"},
	relationScopeJob:     {"job_messages"},
	relationScopeWorker: {
		"analytics_deliveries",
		"email_delivery_events",
		"job_attempts",
		"job_state_transitions",
	},
}

// externalRelationAuthorities is an additive test seam for product services
// that share the physical Postgres service but own their own migrations. A
// consumer must register every such public relation explicitly; otherwise the
// complete inventory test still fails closed. Accounts' request role must not
// acquire DML authority over a relation merely because it shares a database.
type externalRelationAuthority struct {
	owner               string
	appTenantPrivileges relationPrivileges
}

var externalRelationAuthorities = map[string]externalRelationAuthority{}

var appTenantRelationPrivileges = map[string]relationPrivileges{
	// Global catalogs and worker-owned job relations.
	"identity_providers":      {selectRows: true},
	"plans":                   {selectRows: true},
	"plan_entitlements":       {selectRows: true},
	"email_templates":         {selectRows: true},
	"data_retention_policies": {selectRows: true},
	"bootstrap_state":         {selectRows: true, updateRows: true},
	"feature_flags":           {selectRows: true},
	"audit_event_types":       {selectRows: true},
	"platform_admins":         {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"analytics_deliveries":    {},
	"email_delivery_events":   {},
	"job_attempts":            {},
	"job_messages":            {},
	"job_state_transitions":   {},

	// Tenant-scoped relations.
	"actor_chain_journal":                  {selectRows: true, insertRows: true},
	"actor_chain_revocations":              {selectRows: true, insertRows: true},
	"api_keys":                             {selectRows: true, insertRows: true, updateRows: true},
	"approval_decisions":                   {selectRows: true, insertRows: true}, // append-only, like actor_chain_journal
	"approval_requests":                    {selectRows: true, insertRows: true, updateRows: true},
	"audit_events":                         {selectRows: true, insertRows: true},
	"connector_credentials":                {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"delegation_grants":                    {selectRows: true, insertRows: true, updateRows: true},
	"entitlement_overrides":                {selectRows: true, insertRows: true, updateRows: true},
	"invitations":                          {selectRows: true, insertRows: true, updateRows: true},
	"org_identity_providers":               {selectRows: true, insertRows: true, updateRows: true},
	"org_settings":                         {selectRows: true, insertRows: true, updateRows: true},
	"organization_activations":             {selectRows: true, insertRows: true, updateRows: true},
	"organization_authorization_revisions": {selectRows: true},
	"organization_members":                 {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"organizations":                        {selectRows: true, insertRows: true, updateRows: true},
	"principal_authorization_revisions":    {selectRows: true},
	"principals":                           {selectRows: true, insertRows: true, updateRows: true},
	"record_shares":                        {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"role_assignments":                     {selectRows: true, insertRows: true, deleteRows: true},
	"role_permissions":                     {selectRows: true, insertRows: true},
	"roles":                                {selectRows: true, insertRows: true, deleteRows: true},
	"scope_grants":                         {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"scope_nodes":                          {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"subscriptions":                        {selectRows: true, insertRows: true, updateRows: true},
	"team_members":                         {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"teams":                                {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"usage_events":                         {selectRows: true, insertRows: true},
	"usage_totals":                         {selectRows: true, insertRows: true, updateRows: true},
	"webhook_deliveries":                   {selectRows: true, insertRows: true, updateRows: true},
	"webhook_subscriptions":                {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},

	// User-scoped and pre-auth relations.
	"gdpr_requests":          {selectRows: true, insertRows: true, updateRows: true},
	"magic_links":            {},
	"mfa_backup_codes":       {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"mfa_devices":            {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"mfa_login_transactions": {insertRows: true},
	"notifications":          {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"onboarding_progress":    {selectRows: true, insertRows: true, updateRows: true},
	"sessions":               {selectRows: true, insertRows: true, updateRows: true},
	"user_consent_events":    {selectRows: true, insertRows: true},
	"user_consent_preferences": {
		selectRows: true, insertRows: true, updateRows: true,
	},
	"user_identities":      {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"users":                {selectRows: true, insertRows: true, updateRows: true},
	"webauthn_ceremonies":  {selectRows: true, insertRows: true, updateRows: true},
	"webauthn_credentials": {selectRows: true, insertRows: true, updateRows: true},
	"waitlist_entries":     {},
}

func TestRuntimeDatabaseRolesHavePinnedAuthority(t *testing.T) {
	expected := map[string]struct {
		bypassRLS bool
	}{
		"app_tenant":         {bypassRLS: false},
		"app_control_plane":  {bypassRLS: true},
		"app_billing_worker": {bypassRLS: true},
		"app_webhook_worker": {bypassRLS: true},
		"app_job_worker":     {bypassRLS: true},
	}

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		for role, want := range expected {
			var canLogin, superuser, bypassRLS, createDB, createRole, inherit, replication bool
			err := tx.QueryRow(ctx, `
				SELECT rolcanlogin, rolsuper, rolbypassrls, rolcreatedb,
				       rolcreaterole, rolinherit, rolreplication
				FROM pg_roles
				WHERE rolname = $1`, role,
			).Scan(&canLogin, &superuser, &bypassRLS, &createDB, &createRole, &inherit, &replication)
			require.NoError(t, err, role)
			require.False(t, canLogin, role)
			require.False(t, superuser, role)
			require.Equal(t, want.bypassRLS, bypassRLS, role)
			require.False(t, createDB, role)
			require.False(t, createRole, role)
			require.False(t, inherit, role)
			require.False(t, replication, role)
		}
		return nil
	}))
}

func TestRuntimeSessionAndRelationOwnershipAreSeparated(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var sessionRole, currentRole string
		var superuser, bypassRLS, createDB, createRole, replication bool
		err := tx.QueryRow(ctx, `
			SELECT session_user, current_user, role.rolsuper, role.rolbypassrls,
			       role.rolcreatedb, role.rolcreaterole, role.rolreplication
			FROM pg_roles role
			WHERE role.rolname = session_user`,
		).Scan(&sessionRole, &currentRole, &superuser, &bypassRLS, &createDB, &createRole, &replication)
		require.NoError(t, err)
		require.Equal(t, "app_control_plane", currentRole)
		require.NotEqual(t, sessionRole, currentRole, "control-plane work must use a named role")
		require.False(t, superuser, sessionRole)
		require.False(t, bypassRLS, sessionRole)
		require.False(t, createDB, sessionRole)
		require.False(t, createRole, sessionRole)
		require.False(t, replication, sessionRole)

		var migrationOwner string
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT tableowner
			FROM pg_tables
			WHERE schemaname = 'public' AND tablename = 'schema_migrations'`,
		).Scan(&migrationOwner))
		require.NotEqual(t, sessionRole, migrationOwner)

		rows, err := tx.Query(ctx, `
			SELECT DISTINCT tableowner
			FROM pg_tables
			WHERE schemaname = 'public'
			  AND tablename <> 'schema_migrations'`)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var owner string
			require.NoError(t, rows.Scan(&owner))
			require.Equal(t, migrationOwner, owner, "all application relations must be migration-owned")
			require.NotEqual(t, sessionRole, owner, "runtime session must not own application relations")
			require.NotContains(t, []string{
				"app_tenant", "app_control_plane", "app_billing_worker", "app_webhook_worker", "app_job_worker",
			}, owner, "application roles must not own relations")
		}
		return rows.Err()
	}))
}

func TestTenantRoleCannotBypassRLSOrGrowSchemaAuthority(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var canTruncate, canCreate, canUseTemporary, controlPlaneMember, billingMember, webhookMember, jobMember bool
		err := tx.QueryRow(ctx, `
			SELECT has_table_privilege('app_tenant', 'organizations', 'TRUNCATE'),
			       has_schema_privilege('app_tenant', 'public', 'CREATE'),
			       has_database_privilege('app_tenant', current_database(), 'TEMPORARY'),
			       pg_has_role('app_tenant', 'app_control_plane', 'MEMBER'),
			       pg_has_role('app_tenant', 'app_billing_worker', 'MEMBER'),
			       pg_has_role('app_tenant', 'app_webhook_worker', 'MEMBER'),
			       pg_has_role('app_tenant', 'app_job_worker', 'MEMBER')`,
		).Scan(&canTruncate, &canCreate, &canUseTemporary, &controlPlaneMember, &billingMember, &webhookMember, &jobMember)
		require.NoError(t, err)
		require.False(t, canTruncate, "TRUNCATE bypasses tenant RLS")
		require.False(t, canCreate, "tenant runtime must not create schema objects")
		require.False(t, canUseTemporary, "tenant runtime must not create temporary relations")
		require.False(t, controlPlaneMember, "tenant runtime must not assume the control-plane role")
		require.False(t, billingMember, "tenant runtime must not assume the billing worker role")
		require.False(t, webhookMember, "tenant runtime must not assume the webhook projection role")
		require.False(t, jobMember, "tenant runtime must not assume the job worker role")
		return nil
	}))
}

func TestTenantRoleHasNoImplicitFutureTablePrivileges(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var unsafeDefault bool
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(bool_or(
				grantee.rolname = 'app_tenant'
				AND acl.privilege_type IN ('SELECT', 'INSERT', 'UPDATE', 'DELETE', 'TRUNCATE')
			), false)
			FROM pg_default_acl defaults
			CROSS JOIN LATERAL aclexplode(defaults.defaclacl) acl
			LEFT JOIN pg_roles grantee ON grantee.oid = acl.grantee
			WHERE defaults.defaclnamespace = 'public'::regnamespace
			  AND defaults.defaclobjtype = 'r'`,
		).Scan(&unsafeDefault)
		require.NoError(t, err)
		require.False(t, unsafeDefault, "new tables require explicit migration-owned grants")
		return nil
	}))
}

func TestTenantRoleRelationGrantsAreExact(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		for relation, want := range appTenantRelationPrivileges {
			var got relationPrivileges
			var truncateRows bool
			err := tx.QueryRow(ctx, `
				SELECT has_table_privilege('app_tenant', $1, 'SELECT'),
				       has_table_privilege('app_tenant', $1, 'INSERT'),
				       has_table_privilege('app_tenant', $1, 'UPDATE'),
				       has_table_privilege('app_tenant', $1, 'DELETE'),
				       has_table_privilege('app_tenant', $1, 'TRUNCATE')`, relation,
			).Scan(&got.selectRows, &got.insertRows, &got.updateRows, &got.deleteRows, &truncateRows)
			require.NoError(t, err, relation)
			require.Equal(t, want, got, relation)
			require.False(t, truncateRows, relation)
		}
		return nil
	}))
}

func TestControlPlaneRelationGrantsAreExact(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		for relation, scope := range relationScopeInventory(t) {
			want := relationPrivileges{
				selectRows: true,
				insertRows: true,
				updateRows: true,
				deleteRows: true,
			}
			if scope == relationScopeWorker || scope == relationScopeJob {
				want = relationPrivileges{}
			}
			if relation == "feature_flags" {
				want = relationPrivileges{selectRows: true}
			}
			// audit_events is append-only: the control plane reads and inserts
			// (system NULL-org events) but never updates or deletes rows.
			// Retention drops whole partitions via a SECURITY DEFINER function,
			// not a row DELETE, so no DELETE grant is needed.
			if relation == "audit_events" {
				want = relationPrivileges{selectRows: true, insertRows: true}
			}
			// actor_chain_journal / actor_chain_revocations are append-only
			// like audit_events: the control plane reads and inserts but never
			// updates or deletes (an immutable trigger rejects those anyway).
			if relation == "actor_chain_journal" || relation == "actor_chain_revocations" {
				want = relationPrivileges{selectRows: true, insertRows: true}
			}
			// approval_requests is the mutable head: the control plane reads,
			// inserts, and transitions it, but never deletes — org deletion cascades
			// via the org_id FK, so no row-DELETE grant is needed.
			if relation == "approval_requests" {
				want = relationPrivileges{selectRows: true, insertRows: true, updateRows: true}
			}
			// approval_decisions is append-only like actor_chain_journal: read and
			// insert, never update or delete (an immutable trigger rejects those).
			if relation == "approval_decisions" {
				want = relationPrivileges{selectRows: true, insertRows: true}
			}

			var got relationPrivileges
			var truncateRows bool
			err := tx.QueryRow(ctx, `
				SELECT has_table_privilege('app_control_plane', $1, 'SELECT'),
				       has_table_privilege('app_control_plane', $1, 'INSERT'),
				       has_table_privilege('app_control_plane', $1, 'UPDATE'),
				       has_table_privilege('app_control_plane', $1, 'DELETE'),
				       has_table_privilege('app_control_plane', $1, 'TRUNCATE')`, relation,
			).Scan(&got.selectRows, &got.insertRows, &got.updateRows, &got.deleteRows, &truncateRows)
			require.NoError(t, err, relation)
			require.Equal(t, want, got, relation)
			require.False(t, truncateRows, relation)
		}

		var canCreate, canUseTemporary, billingMember, webhookMember, jobMember, unsafeDefault bool
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT has_schema_privilege('app_control_plane', 'public', 'CREATE'),
			       has_database_privilege('app_control_plane', current_database(), 'TEMPORARY'),
			       pg_has_role('app_control_plane', 'app_billing_worker', 'MEMBER'),
			       pg_has_role('app_control_plane', 'app_webhook_worker', 'MEMBER'),
			       pg_has_role('app_control_plane', 'app_job_worker', 'MEMBER'),
			       (
			           SELECT COALESCE(bool_or(
			               grantee.rolname = 'app_control_plane'
			               AND acl.privilege_type IN ('SELECT', 'INSERT', 'UPDATE', 'DELETE', 'TRUNCATE')
			           ), false)
			           FROM pg_default_acl defaults
			           CROSS JOIN LATERAL aclexplode(defaults.defaclacl) acl
			           LEFT JOIN pg_roles grantee ON grantee.oid = acl.grantee
			           WHERE defaults.defaclnamespace = 'public'::regnamespace
			             AND defaults.defaclobjtype = 'r'
			       )`,
		).Scan(&canCreate, &canUseTemporary, &billingMember, &webhookMember, &jobMember, &unsafeDefault))
		require.False(t, canCreate)
		require.False(t, canUseTemporary)
		require.False(t, billingMember)
		require.False(t, webhookMember)
		require.False(t, jobMember)
		require.False(t, unsafeDefault, "new tables require explicit control-plane grants")
		return nil
	}))
}

func TestBillingWorkerRoleHasProjectionOnlyAuthority(t *testing.T) {
	expected := map[string]relationPrivileges{
		"plans":                 {selectRows: true},
		"organizations":         {selectRows: true},
		"users":                 {selectRows: true},
		"subscriptions":         {selectRows: true, insertRows: true, updateRows: true},
		"job_messages":          {},
		"job_attempts":          {},
		"job_state_transitions": {},
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		for relation, want := range expected {
			var got relationPrivileges
			var truncateRows bool
			require.NoError(t, tx.QueryRow(ctx, `
				SELECT has_table_privilege('app_billing_worker', $1, 'SELECT'),
				       has_table_privilege('app_billing_worker', $1, 'INSERT'),
				       has_table_privilege('app_billing_worker', $1, 'UPDATE'),
				       has_table_privilege('app_billing_worker', $1, 'DELETE'),
				       has_table_privilege('app_billing_worker', $1, 'TRUNCATE')`, relation,
			).Scan(
				&got.selectRows, &got.insertRows, &got.updateRows, &got.deleteRows, &truncateRows,
			), relation)
			require.Equal(t, want, got, relation)
			require.False(t, truncateRows, relation)
		}
		return nil
	}))
}

func TestWebhookProjectionRoleHasProjectionOnlyAuthority(t *testing.T) {
	expected := map[string]relationPrivileges{
		"webhook_subscriptions": {selectRows: true},
		"webhook_deliveries":    {selectRows: true},
	}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		for relation := range relationScopeInventory(t) {
			var got relationPrivileges
			var truncateRows bool
			require.NoError(t, tx.QueryRow(ctx, `
				SELECT has_table_privilege('app_webhook_worker', $1, 'SELECT'),
				       has_table_privilege('app_webhook_worker', $1, 'INSERT'),
				       has_table_privilege('app_webhook_worker', $1, 'UPDATE'),
				       has_table_privilege('app_webhook_worker', $1, 'DELETE'),
				       has_table_privilege('app_webhook_worker', $1, 'TRUNCATE')`, relation,
			).Scan(
				&got.selectRows, &got.insertRows, &got.updateRows, &got.deleteRows, &truncateRows,
			), relation)
			require.Equal(t, expected[relation], got, relation)
			require.False(t, truncateRows, relation)
		}
		var canCreate, canUseTemporary bool
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT has_schema_privilege('app_webhook_worker', 'public', 'CREATE'),
			       has_database_privilege('app_webhook_worker', current_database(), 'TEMPORARY')`,
		).Scan(&canCreate, &canUseTemporary))
		require.False(t, canCreate)
		require.False(t, canUseTemporary)

		mutableColumns := map[string]bool{
			"status": true, "http_status": true, "response_body": true,
			"attempts": true, "last_attempt_at": true,
			"delivered_at": true, "updated_at": true,
		}
		rows, err := tx.Query(ctx, `
			SELECT column_name,
			       has_column_privilege(
			           'app_webhook_worker', 'webhook_deliveries', column_name, 'UPDATE'
			       )
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'webhook_deliveries'`)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var column string
			var canUpdate bool
			require.NoError(t, rows.Scan(&column, &canUpdate))
			require.Equal(t, mutableColumns[column], canUpdate, column)
		}
		require.NoError(t, rows.Err())
		return nil
	}))
}

func TestAnalyticsDeliveryRoleHasProjectionOnlyAuthority(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var canSelect, canInsert, canUpdate, canDelete, canTruncate bool
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT has_table_privilege('app_job_worker', 'analytics_deliveries', 'SELECT'),
			       has_table_privilege('app_job_worker', 'analytics_deliveries', 'INSERT'),
			       has_table_privilege('app_job_worker', 'analytics_deliveries', 'UPDATE'),
			       has_table_privilege('app_job_worker', 'analytics_deliveries', 'DELETE'),
			       has_table_privilege('app_job_worker', 'analytics_deliveries', 'TRUNCATE')`,
		).Scan(&canSelect, &canInsert, &canUpdate, &canDelete, &canTruncate))
		require.True(t, canSelect)
		require.True(t, canInsert)
		require.False(t, canUpdate)
		require.False(t, canDelete)
		require.False(t, canTruncate)

		mutableColumns := map[string]bool{
			"provider_reference": true,
			"duplicate":          true,
			"delivered_at":       true,
		}
		rows, err := tx.Query(ctx, `
			SELECT column_name,
			       has_column_privilege(
			           'app_job_worker', 'analytics_deliveries', column_name, 'UPDATE'
			       )
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'analytics_deliveries'`)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var column string
			var canUpdateColumn bool
			require.NoError(t, rows.Scan(&column, &canUpdateColumn))
			require.Equal(t, mutableColumns[column], canUpdateColumn, column)
		}
		return rows.Err()
	}))
}

func TestDatabaseRelationAuthorityInventoryIsComplete(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		// relispartition excludes the audit_events monthly partition children:
		// they are dynamically named and inherit access through the partitioned
		// parent, so they carry no independent authority to classify.
		rows, err := tx.Query(ctx, `
			SELECT c.relname
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public'
			  AND c.relkind IN ('r', 'p')
			  AND NOT c.relispartition
			  AND c.relname <> 'schema_migrations'
			ORDER BY c.relname`)
		require.NoError(t, err)
		defer rows.Close()

		var actual []string
		for rows.Next() {
			var relation string
			require.NoError(t, rows.Scan(&relation))
			actual = append(actual, relation)
		}
		require.NoError(t, rows.Err())

		scopes := relationScopeInventory(t)
		expected := make([]string, 0, len(scopes)+len(externalRelationAuthorities))
		for relation := range scopes {
			expected = append(expected, relation)
		}
		for relation, authority := range externalRelationAuthorities {
			_, baseRelation := scopes[relation]
			require.False(t, baseRelation, "%s cannot be both Accounts-owned and externally owned by %s", relation, authority.owner)
			expected = append(expected, relation)

			var actualPrivileges relationPrivileges
			require.NoError(t, tx.QueryRow(ctx, `
				SELECT has_table_privilege('app_tenant', $1, 'SELECT'),
				       has_table_privilege('app_tenant', $1, 'INSERT'),
				       has_table_privilege('app_tenant', $1, 'UPDATE'),
				       has_table_privilege('app_tenant', $1, 'DELETE')`, relation,
			).Scan(
				&actualPrivileges.selectRows,
				&actualPrivileges.insertRows,
				&actualPrivileges.updateRows,
				&actualPrivileges.deleteRows,
			), relation)
			assert.Equal(t, authority.appTenantPrivileges, actualPrivileges,
				"%s-owned relation %s has unclassified app_tenant authority", authority.owner, relation)

			var controlPlanePrivileges relationPrivileges
			require.NoError(t, tx.QueryRow(ctx, `
				SELECT has_table_privilege('app_control_plane', $1, 'SELECT'),
				       has_table_privilege('app_control_plane', $1, 'INSERT'),
				       has_table_privilege('app_control_plane', $1, 'UPDATE'),
				       has_table_privilege('app_control_plane', $1, 'DELETE')`, relation,
			).Scan(
				&controlPlanePrivileges.selectRows,
				&controlPlanePrivileges.insertRows,
				&controlPlanePrivileges.updateRows,
				&controlPlanePrivileges.deleteRows,
			), relation)
			assert.Equal(t, relationPrivileges{}, controlPlanePrivileges,
				"%s-owned relation %s leaked authority to app_control_plane", authority.owner, relation)
		}
		sort.Strings(expected)
		require.Equal(t, expected, actual, "every public table needs an explicit authority classification")
		require.Len(t, appTenantRelationPrivileges, len(scopes), "scope and privilege inventories must cover the same relations")
		for relation := range scopes {
			_, ok := appTenantRelationPrivileges[relation]
			require.True(t, ok, relation)
		}
		return nil
	}))
}

func TestDatabaseRelationRLSMatchesAuthorityScope(t *testing.T) {
	scopes := relationScopeInventory(t)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		for relation, scope := range scopes {
			var enabled, forced bool
			var policyCount int
			err := tx.QueryRow(ctx, `
				SELECT class.relrowsecurity,
				       class.relforcerowsecurity,
				       COUNT(policy.polname)
				FROM pg_class class
				LEFT JOIN pg_policy policy ON policy.polrelid = class.oid
				WHERE class.oid = $1::regclass
				GROUP BY class.oid, class.relrowsecurity, class.relforcerowsecurity`, relation,
			).Scan(&enabled, &forced, &policyCount)
			require.NoError(t, err, relation)

			requiresRLS := scope == relationScopeTenant || scope == relationScopeUser || scope == relationScopePreAuth || scope == relationScopeJob
			require.Equal(t, requiresRLS, enabled, relation)
			require.Equal(t, requiresRLS, forced, relation)
			if requiresRLS {
				require.Positive(t, policyCount, relation)
			} else {
				require.Zero(t, policyCount, relation)
			}
		}
		return nil
	}))
}

func TestActivePoliciesDoNotTrustSessionBypassSettings(t *testing.T) {
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared transaction context key
		var count int
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM pg_policy policy
			JOIN pg_class class ON class.oid = policy.polrelid
			JOIN pg_namespace namespace ON namespace.oid = class.relnamespace
			WHERE namespace.nspname = 'public'
			  AND (
			      COALESCE(pg_get_expr(policy.polqual, policy.polrelid), '') LIKE '%app.bypass%'
			      OR COALESCE(pg_get_expr(policy.polwithcheck, policy.polrelid), '') LIKE '%app.bypass%'
			  )`,
		).Scan(&count))
		require.Zero(t, count, "custom session settings must never grant RLS authority")
		return nil
	}))
}

func relationScopeInventory(t *testing.T) map[string]relationScope {
	t.Helper()
	out := make(map[string]relationScope, len(appTenantRelationPrivileges))
	for scope, relations := range relationsByScope {
		for _, relation := range relations {
			_, exists := out[relation]
			require.False(t, exists, "relation %s has multiple scope classifications", relation)
			out[relation] = scope
		}
	}
	return out
}
