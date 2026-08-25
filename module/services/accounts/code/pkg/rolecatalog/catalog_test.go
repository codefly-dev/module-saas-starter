package rolecatalog_test

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/rolecatalog"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files with the current output")

func TestParseExampleCatalog(t *testing.T) {
	document, err := os.ReadFile("testdata/example_catalog.json")
	require.NoError(t, err)

	catalog, err := rolecatalog.Parse(document)
	require.NoError(t, err)
	require.Equal(t, uint32(1), catalog.Version)
	require.Len(t, catalog.Roles, 1)

	role := catalog.Roles[0]
	require.Equal(t, "module-a:analyst", role.Name)
	require.Equal(t, "module-a", role.Scope)
	// Permissions are canonically sorted regardless of source order.
	require.Equal(t, []rolecatalog.Permission{
		{Resource: "queries", Action: "execute"},
		{Resource: "reports", Action: "read"},
	}, role.Permissions)
}

func TestParseRejectsInvalidDocuments(t *testing.T) {
	cases := map[string]string{
		"wrong version":    `{"version": 2, "roles": []}`,
		"unknown field":    `{"version": 1, "roles": [], "extra": true}`,
		"trailing content": `{"version": 1, "roles": []}{}`,
		"empty role name":  `{"version": 1, "roles": [{"name": "", "permissions": []}]}`,
		"whitespace name":  `{"version": 1, "roles": [{"name": "a b", "permissions": []}]}`,
		"duplicate role":   `{"version": 1, "roles": [{"name": "a", "permissions": []}, {"name": "a", "permissions": []}]}`,
		"empty resource":   `{"version": 1, "roles": [{"name": "a", "permissions": [{"resource": "", "action": "read"}]}]}`,
		"empty action":     `{"version": 1, "roles": [{"name": "a", "permissions": [{"resource": "x", "action": ""}]}]}`,
		"bad scope":        `{"version": 1, "roles": [{"name": "a", "scope": "a b", "permissions": []}]}`,
		"duplicate perm":   `{"version": 1, "roles": [{"name": "a", "permissions": [{"resource": "x", "action": "y"}, {"resource": "x", "action": "y"}]}]}`,
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := rolecatalog.Parse([]byte(document))
			require.Error(t, err)
		})
	}
}

func TestParseAllowsWildcardAndEmptyCatalog(t *testing.T) {
	catalog, err := rolecatalog.Parse([]byte(`{"version": 1, "roles": []}`))
	require.NoError(t, err)
	require.Empty(t, catalog.Roles)

	catalog, err = rolecatalog.Parse([]byte(
		`{"version": 1, "roles": [{"name": "admin", "permissions": [{"resource": "*", "action": "*"}]}]}`))
	require.NoError(t, err)
	require.Equal(t, "*", catalog.Roles[0].Permissions[0].Resource)
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	a, err := rolecatalog.Parse([]byte(`{"version":1,"roles":[
		{"name":"b","permissions":[{"resource":"z","action":"read"},{"resource":"a","action":"read"}]},
		{"name":"a","permissions":[]}]}`))
	require.NoError(t, err)
	// Same content, different source ordering → identical fingerprint.
	b, err := rolecatalog.Parse([]byte(`{"version":1,"roles":[
		{"name":"a","permissions":[]},
		{"name":"b","permissions":[{"resource":"a","action":"read"},{"resource":"z","action":"read"}]}]}`))
	require.NoError(t, err)
	require.Equal(t, a.Fingerprint(), b.Fingerprint())
	require.Len(t, a.Fingerprint(), 64)

	// A real change moves the fingerprint.
	c, err := rolecatalog.Parse([]byte(`{"version":1,"roles":[
		{"name":"a","permissions":[]},
		{"name":"b","permissions":[{"resource":"a","action":"write"},{"resource":"z","action":"read"}]}]}`))
	require.NoError(t, err)
	require.NotEqual(t, a.Fingerprint(), c.Fingerprint())
}

func TestParseCanonicalizesOrdering(t *testing.T) {
	a, err := rolecatalog.Parse([]byte(`{"version":1,"roles":[
		{"name":"b","permissions":[{"resource":"z","action":"read"},{"resource":"a","action":"read"}]},
		{"name":"a","permissions":[]}]}`))
	require.NoError(t, err)
	b, err := rolecatalog.Parse([]byte(`{"version":1,"roles":[
		{"name":"a","permissions":[]},
		{"name":"b","permissions":[{"resource":"a","action":"read"},{"resource":"z","action":"read"}]}]}`))
	require.NoError(t, err)

	// Equivalent documents plan identically regardless of source ordering.
	require.Equal(t,
		rolecatalog.Diff(rolecatalog.State{}, a).Format(),
		rolecatalog.Diff(rolecatalog.State{}, b).Format())
}

func TestDiffNoOpAndAdoption(t *testing.T) {
	state := rolecatalog.State{Roles: []rolecatalog.StateRole{
		{
			ID: "1", Name: "module-a:analyst", CatalogManaged: true,
			Description: "Read access to module A", Scope: "module-a",
			Permissions: []rolecatalog.Permission{{Resource: "reports", Action: "read"}},
		},
		{ID: "2", Name: "admin", CatalogManaged: false, Description: "seed", Permissions: []rolecatalog.Permission{{Resource: "*", Action: "*"}}},
	}}
	catalog := &rolecatalog.Catalog{Version: 1, Roles: []rolecatalog.Role{
		{
			Name: "module-a:analyst", Description: "Read access to module A", Scope: "module-a",
			Permissions: []rolecatalog.Permission{{Resource: "reports", Action: "read"}},
		},
	}}

	// analyst matches exactly and admin is not named → nothing changes.
	require.True(t, rolecatalog.Diff(state, catalog).Empty())

	// Naming admin adopts it into catalog management even with identical fields.
	catalog.Roles = append(catalog.Roles, rolecatalog.Role{
		Name: "admin", Description: "seed", Permissions: []rolecatalog.Permission{{Resource: "*", Action: "*"}},
	})
	plan := rolecatalog.Diff(state, catalog)
	require.Len(t, plan.Updates, 1)
	require.True(t, plan.Updates[0].AdoptManaged)
	require.Nil(t, plan.Updates[0].DescriptionChange)
}

func TestDiffOrphanGuard(t *testing.T) {
	state := rolecatalog.State{Roles: []rolecatalog.StateRole{
		{ID: "1", Name: "legacy:viewer", CatalogManaged: true, AssignmentCount: 3,
			Permissions: []rolecatalog.Permission{{Resource: "x", Action: "read"}}},
	}}
	plan := rolecatalog.Diff(state, &rolecatalog.Catalog{Version: 1})
	require.Len(t, plan.Removes, 1)
	require.Len(t, plan.OrphaningRemovals(), 1)
}

func TestPlanFormatGolden(t *testing.T) {
	state := rolecatalog.State{Roles: []rolecatalog.StateRole{
		{ID: "1", Name: "admin", CatalogManaged: false, Description: "Full access to all resources",
			Permissions: []rolecatalog.Permission{{Resource: "*", Action: "*"}}},
		{ID: "2", Name: "editor", CatalogManaged: false,
			Permissions: []rolecatalog.Permission{{Resource: "docs", Action: "write"}}},
		{ID: "3", Name: "module-a:analyst", CatalogManaged: true, Description: "Read access to module A", Scope: "module-a",
			Permissions: []rolecatalog.Permission{{Resource: "queries", Action: "execute"}, {Resource: "reports", Action: "read"}}},
		{ID: "4", Name: "module-b:writer", CatalogManaged: true, Description: "old", Scope: "module-b",
			Permissions: []rolecatalog.Permission{{Resource: "docs", Action: "delete"}, {Resource: "docs", Action: "read"}}},
		{ID: "5", Name: "legacy:auditor", CatalogManaged: true,
			Permissions: []rolecatalog.Permission{{Resource: "audit", Action: "read"}}},
		{ID: "6", Name: "legacy:viewer", CatalogManaged: true, AssignmentCount: 3,
			Permissions: []rolecatalog.Permission{{Resource: "x", Action: "read"}}},
	}}
	catalog := &rolecatalog.Catalog{Version: 1, Roles: []rolecatalog.Role{
		{Name: "admin", Description: "Full platform access", Permissions: []rolecatalog.Permission{{Resource: "*", Action: "*"}}},
		{Name: "module-a:analyst", Description: "Read access to module A", Scope: "module-a",
			Permissions: []rolecatalog.Permission{{Resource: "queries", Action: "execute"}, {Resource: "reports", Action: "read"}}},
		{Name: "module-b:writer", Description: "new", Scope: "module-b",
			Permissions: []rolecatalog.Permission{{Resource: "docs", Action: "read"}, {Resource: "docs", Action: "write"}}},
		{Name: "module-c:admin", Description: "Admin of module C", Scope: "module-c",
			Permissions: []rolecatalog.Permission{{Resource: "*", Action: "*"}}},
	}}

	got := rolecatalog.Diff(state, catalog).Format()

	if *updateGolden {
		require.NoError(t, os.WriteFile("testdata/plan.golden", []byte(got), 0o644))
	}
	want, err := os.ReadFile("testdata/plan.golden")
	require.NoError(t, err, "regenerate with: go test ./pkg/rolecatalog -run TestPlanFormatGolden -update")
	require.Equal(t, string(want), got, "plan format drifted; regenerate with -update")
}
