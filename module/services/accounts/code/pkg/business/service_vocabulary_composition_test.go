package business

import (
	"testing"

	"accounts/pkg/permissioncatalog"

	"github.com/stretchr/testify/require"
)

func TestComposedPermissionVocabularyPublishesContributedPermissions(t *testing.T) {
	definitions, err := composedPermissionVocabulary(
		[]servicePermissionDefinition{{Permission: "base:read", Description: "base"}},
		[]permissioncatalog.Permission{{Name: "product.documents:write", Resource: "product.documents", Action: "write"}},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"base:read", "product.documents:write"}, []string{
		definitions[0].Permission,
		definitions[1].Permission,
	})
}

func TestComposedPermissionVocabularyRejectsMalformedOrCollidingContributions(t *testing.T) {
	base := []servicePermissionDefinition{{Permission: "base:read"}}
	for name, permission := range map[string]permissioncatalog.Permission{
		"malformed": {Name: "product:read", Resource: "product", Action: "write"},
		"collision": {Name: "base:read", Resource: "base", Action: "read"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := composedPermissionVocabulary(base, []permissioncatalog.Permission{permission})
			require.Error(t, err)
		})
	}
}

func TestMemberPermissionIndexHoldsOnlyDeclaredContributedPairs(t *testing.T) {
	index := memberPermissionIndex([]permissioncatalog.Permission{
		{Name: "example.jobs:list", Resource: "example.jobs", Action: "list", Members: true},
		{Name: "example.jobs:inspect", Resource: "example.jobs", Action: "inspect"},
	})
	require.Equal(t, map[string]struct{}{"example.jobs:list": {}}, index)
}

func TestComposedPermissionVocabularyNamesTheMemberGrant(t *testing.T) {
	definitions, err := composedPermissionVocabulary(nil, []permissioncatalog.Permission{
		{Name: "example.jobs:list", Resource: "example.jobs", Action: "list", Members: true},
		{Name: "example.jobs:inspect", Resource: "example.jobs", Action: "inspect"},
	})
	require.NoError(t, err)
	require.Equal(t, "example.jobs:inspect", definitions[0].Permission)
	require.Empty(t, definitions[0].BuiltInRoles)
	require.Equal(t, []string{"admin (via membership)", "member (via membership)"}, definitions[1].BuiltInRoles)
}

func TestTheBaseCatalogGrantsNothingToMembers(t *testing.T) {
	require.False(t, IsMemberPermission("*", "*"))
	require.False(t, IsMemberPermission("users", "read"))
}
