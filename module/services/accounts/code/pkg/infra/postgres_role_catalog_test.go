package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/infra"
	"accounts/pkg/rolecatalog"
)

// resetCatalogRoles clears catalog-managed built-in roles so each test starts
// from a known catalog namespace. The importer reasons over ALL catalog-managed
// built-ins, so leftovers from another test would otherwise be removal
// candidates. Seed admin/editor/viewer (catalog_managed = false) are untouched.
func resetCatalogRoles(t *testing.T) {
	t.Helper()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		_, err := tx.Exec(ctx, `DELETE FROM roles WHERE catalog_managed = true`)
		return err
	}))
}

func parseCatalog(t *testing.T, document string) *rolecatalog.Catalog {
	t.Helper()
	catalog, err := rolecatalog.Parse([]byte(document))
	require.NoError(t, err)
	return catalog
}

type builtinRole struct {
	id             string
	description    string
	scope          string
	builtIn        bool
	catalogManaged bool
	orgIsNull      bool
	permissions    []string
}

func readBuiltinRole(t *testing.T, name string) (builtinRole, bool) {
	t.Helper()
	var role builtinRole
	found := false
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		var (
			desc, scope *string
		)
		err := tx.QueryRow(ctx, `
			SELECT id, description, scope, built_in, catalog_managed, org_id IS NULL
			FROM roles WHERE name = $1 AND org_id IS NULL`, name).
			Scan(&role.id, &desc, &scope, &role.builtIn, &role.catalogManaged, &role.orgIsNull)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if desc != nil {
			role.description = *desc
		}
		if scope != nil {
			role.scope = *scope
		}
		rows, err := tx.Query(ctx, `SELECT resource || ':' || action FROM role_permissions WHERE role_id = $1 ORDER BY 1`, role.id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			role.permissions = append(role.permissions, p)
		}
		return rows.Err()
	}))
	return role, found
}

func countCatalogAudits(t *testing.T, action string) int {
	t.Helper()
	var n int
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		return tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM audit_events
			WHERE actor_type = 'system' AND resource = 'role' AND action = $1`, action).Scan(&n)
	}))
	return n
}

func readLatestAuditMetadata(t *testing.T, action, roleID string) map[string]string {
	t.Helper()
	metadata := map[string]string{}
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		var raw []byte
		err := tx.QueryRow(ctx, `
			SELECT metadata FROM audit_events
			WHERE resource = 'role' AND resource_id = $1 AND action = $2
			ORDER BY created_at DESC, id DESC LIMIT 1`, roleID, action).Scan(&raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &metadata)
	}))
	return metadata
}

const exampleCatalogPath = "../rolecatalog/testdata/example_catalog.json"

func TestImportRoleCatalogAppliesExampleThenNoOp(t *testing.T) {
	resetCatalogRoles(t)
	document, err := os.ReadFile(exampleCatalogPath)
	require.NoError(t, err)
	catalog, err := rolecatalog.Parse(document)
	require.NoError(t, err)

	result, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Len(t, result.Plan.Creates, 1)

	role, found := readBuiltinRole(t, "module-a:analyst")
	require.True(t, found)
	require.True(t, role.builtIn)
	require.True(t, role.orgIsNull)
	require.True(t, role.catalogManaged)
	require.Equal(t, "module-a", role.scope)
	require.Equal(t, "Read access to module A", role.description)
	require.Equal(t, []string{"queries:execute", "reports:read"}, role.permissions)

	// Seed built-ins are left alone.
	for _, seed := range []string{"admin", "editor", "viewer"} {
		seedRole, ok := readBuiltinRole(t, seed)
		require.True(t, ok, "seed role %q must survive import", seed)
		require.False(t, seedRole.catalogManaged, "seed role %q must not be adopted", seed)
	}

	// Second run is a true no-op.
	again, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{})
	require.NoError(t, err)
	require.True(t, again.Plan.Empty())
}

func TestImportRoleCatalogOnePermissionChangeIsOneRowOneAudit(t *testing.T) {
	resetCatalogRoles(t)
	base := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:reader","description":"d","scope":"m",
		 "permissions":[{"resource":"reports","action":"read"}]}]}`)
	_, err := testStore.ImportRoleCatalog(testCtx, base, infra.ImportOptions{})
	require.NoError(t, err)

	role, found := readBuiltinRole(t, "catalog-test:reader")
	require.True(t, found)
	require.Len(t, role.permissions, 1)

	updatesBefore := countCatalogAudits(t, "role.updated")

	// Add exactly one permission.
	changed := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:reader","description":"d","scope":"m",
		 "permissions":[{"resource":"reports","action":"read"},{"resource":"reports","action":"write"}]}]}`)
	result, err := testStore.ImportRoleCatalog(testCtx, changed, infra.ImportOptions{})
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Len(t, result.Plan.Updates, 1)
	require.Equal(t, []rolecatalog.Permission{{Resource: "reports", Action: "write"}}, result.Plan.Updates[0].AddPermissions)
	require.False(t, result.Plan.Updates[0].RowChanged(), "only a permission changed; the roles row must not be rewritten")

	after, found := readBuiltinRole(t, "catalog-test:reader")
	require.True(t, found)
	require.Equal(t, []string{"reports:read", "reports:write"}, after.permissions)
	require.Equal(t, role.id, after.id, "role identity is stable across permission edits")

	require.Equal(t, updatesBefore+1, countCatalogAudits(t, "role.updated"), "exactly one update audit event")

	// Removing a single permission is likewise one row change and one event.
	reverted, err := testStore.ImportRoleCatalog(testCtx, base, infra.ImportOptions{})
	require.NoError(t, err)
	require.True(t, reverted.Applied)
	require.Len(t, reverted.Plan.Updates, 1)
	require.Equal(t, []rolecatalog.Permission{{Resource: "reports", Action: "write"}}, reverted.Plan.Updates[0].RemovePermissions)
	require.Empty(t, reverted.Plan.Updates[0].AddPermissions)
	require.False(t, reverted.Plan.Updates[0].RowChanged())

	final, found := readBuiltinRole(t, "catalog-test:reader")
	require.True(t, found)
	require.Equal(t, []string{"reports:read"}, final.permissions)
	require.Equal(t, updatesBefore+2, countCatalogAudits(t, "role.updated"))
}

func TestImportRoleCatalogLeavesCustomRolesAndAssignmentsUntouched(t *testing.T) {
	resetCatalogRoles(t)
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	// A custom, org-scoped role and an assignment to it — the importer must never
	// touch either.
	customRoleID := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		if _, err := tx.Exec(ctx, `
			INSERT INTO roles (id, name, description, built_in, org_id, catalog_managed)
			VALUES ($1, 'custom-analyst', 'custom', false, $2, false)`, customRoleID, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_permissions (role_id, resource, action) VALUES ($1, 'reports', 'read')`, customRoleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO role_assignments (id, subject_id, subject_kind, role_id, org_id)
			VALUES ($1, $2, 'principal', $3, $4)`, business.NewIDString(), userID, customRoleID, orgID)
		return err
	}))

	catalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:writer","permissions":[{"resource":"docs","action":"write"}]}]}`)
	_, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{})
	require.NoError(t, err)

	// The custom role and its assignment are intact.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		var roleCount, assignmentCount int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM roles WHERE id = $1`, customRoleID).Scan(&roleCount); err != nil {
			return err
		}
		require.Equal(t, 1, roleCount)
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM role_assignments WHERE role_id = $1`, customRoleID).Scan(&assignmentCount); err != nil {
			return err
		}
		require.Equal(t, 1, assignmentCount)
		return nil
	}))
}

func TestImportRoleCatalogRefusesOrphaningRemovalUnlessForced(t *testing.T) {
	resetCatalogRoles(t)
	userID := seedUser(t)
	orgID := seedOrg(t, userID)

	// Seed two catalog-managed roles and assign one of them.
	seedCatalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:keep","permissions":[{"resource":"y","action":"read"}]},
		{"name":"catalog-test:temp","permissions":[{"resource":"x","action":"read"}]}]}`)
	_, err := testStore.ImportRoleCatalog(testCtx, seedCatalog, infra.ImportOptions{})
	require.NoError(t, err)
	role, found := readBuiltinRole(t, "catalog-test:temp")
	require.True(t, found)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		_, err := tx.Exec(ctx, `
			INSERT INTO role_assignments (id, subject_id, subject_kind, role_id, org_id)
			VALUES ($1, $2, 'principal', $3, $4)`, business.NewIDString(), userID, role.id, orgID)
		return err
	}))

	// A non-empty catalog that drops the assigned role: the orphan guard (not
	// the empty-catalog guard) refuses it without force.
	dropTemp := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:keep","permissions":[{"resource":"y","action":"read"}]}]}`)
	refused, err := testStore.ImportRoleCatalog(testCtx, dropTemp, infra.ImportOptions{})
	require.NoError(t, err)
	require.True(t, refused.Refused)
	require.Contains(t, refused.RefusalReason, "orphan")
	require.False(t, refused.Applied)
	_, stillThere := readBuiltinRole(t, "catalog-test:temp")
	require.True(t, stillThere, "refused import must not remove anything")

	// Force applies the removal; the assignment cascades away.
	forced, err := testStore.ImportRoleCatalog(testCtx, dropTemp, infra.ImportOptions{Force: true})
	require.NoError(t, err)
	require.True(t, forced.Applied)
	_, gone := readBuiltinRole(t, "catalog-test:temp")
	require.False(t, gone)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM role_assignments WHERE role_id = $1`, role.id).Scan(&n); err != nil {
			return err
		}
		require.Equal(t, 0, n)
		return nil
	}))
}

func TestImportRoleCatalogRefusesEmptyCatalogWipeUnlessForced(t *testing.T) {
	resetCatalogRoles(t)

	// A catalog-managed role with NO assignments — the orphan guard alone would
	// not protect it, so an empty catalog would previously delete it silently.
	seedCatalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:unassigned","permissions":[{"resource":"x","action":"read"}]}]}`)
	_, err := testStore.ImportRoleCatalog(testCtx, seedCatalog, infra.ImportOptions{})
	require.NoError(t, err)

	empty := parseCatalog(t, `{"version":1,"roles":[]}`)
	refused, err := testStore.ImportRoleCatalog(testCtx, empty, infra.ImportOptions{})
	require.NoError(t, err)
	require.True(t, refused.Refused, "empty catalog must not silently wipe catalog-managed roles")
	require.Contains(t, refused.RefusalReason, "no roles")
	require.False(t, refused.Applied)
	_, stillThere := readBuiltinRole(t, "catalog-test:unassigned")
	require.True(t, stillThere)

	// Force makes the wipe a deliberate act.
	forced, err := testStore.ImportRoleCatalog(testCtx, empty, infra.ImportOptions{Force: true})
	require.NoError(t, err)
	require.True(t, forced.Applied)
	_, gone := readBuiltinRole(t, "catalog-test:unassigned")
	require.False(t, gone)
}

func TestImportRoleCatalogStampsProvenanceOnAudit(t *testing.T) {
	resetCatalogRoles(t)
	catalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:provenance","permissions":[{"resource":"x","action":"read"}]}]}`)

	_, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{Source: "roles.json"})
	require.NoError(t, err)

	role, found := readBuiltinRole(t, "catalog-test:provenance")
	require.True(t, found)

	metadata := readLatestAuditMetadata(t, "role.created", role.id)
	require.Equal(t, catalog.Fingerprint(), metadata["catalog_sha256"])
	require.Equal(t, "roles.json", metadata["catalog_source"])
	require.Equal(t, "catalog-test:provenance", metadata["name"])
}

func TestBuiltinRoleNamesAreUnique(t *testing.T) {
	err := testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		_, execErr := tx.Exec(ctx, `
			INSERT INTO roles (id, name, description, built_in, org_id)
			VALUES ($1, 'admin', 'duplicate built-in', true, NULL)`, business.NewIDString())
		return execErr
	})
	require.Error(t, err, "a second built-in role named 'admin' must violate the partial unique index")
}

func TestImportRoleCatalogDryRunWritesNothing(t *testing.T) {
	resetCatalogRoles(t)
	catalog := parseCatalog(t, `{"version":1,"roles":[
		{"name":"catalog-test:dryrun","permissions":[{"resource":"x","action":"read"}]}]}`)

	result, err := testStore.ImportRoleCatalog(testCtx, catalog, infra.ImportOptions{DryRun: true})
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Len(t, result.Plan.Creates, 1)

	_, found := readBuiltinRole(t, "catalog-test:dryrun")
	require.False(t, found, "dry-run must not create the role")
}
