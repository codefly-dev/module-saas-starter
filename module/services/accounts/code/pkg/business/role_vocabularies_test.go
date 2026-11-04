package business

import (
	"strings"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/stretchr/testify/require"
)

// The three vocabularies are read from the code that defines each, never
// typed: every word names something the source declares, and nothing the
// source declares is missing from the table.
func TestRoleVocabulariesAreReadFromTheirSources(t *testing.T) {
	byName := map[string]RoleVocabulary{}
	for _, v := range RoleVocabularies() {
		byName[v.Name] = v
	}
	require.Len(t, byName, 3)

	organisation := byName["Organisation role"].Words
	require.Equal(t, []string{"owner", "admin", "member"}, organisation)
	for _, word := range organisation {
		_, declared := gen.OrgRole_value["ORG_ROLE_"+strings.ToUpper(word)]
		require.True(t, declared, "%q is not an OrgRole", word)
	}
	require.Len(t, organisation, len(gen.OrgRole_name)-1, "every OrgRole but UNSPECIFIED is a word")

	catalogue := byName["Catalogue built-in role"].Words
	require.Equal(t, []string{"admin", "editor", "viewer"}, catalogue)
	for _, word := range catalogue {
		granted := false
		for _, definition := range servicePermissionVocabulary {
			for _, holder := range definition.BuiltInRoles {
				if name, _, _ := strings.Cut(holder, " ("); name == word {
					granted = true
				}
			}
		}
		require.True(t, granted, "%q holds no permission in the vocabulary", word)
	}
	for _, v := range RoleVocabularies() {
		require.NotContains(t, v.Words, "super_admin", "a platform role is none of the three vocabularies")
	}

	require.Equal(t, []string{domain.InstallerRole}, byName["Installation role"].Words)

	rendered := renderRoleVocabularies()
	for _, v := range RoleVocabularies() {
		require.Contains(t, rendered, "| "+v.Name+" | ")
		for _, word := range v.Words {
			require.Contains(t, rendered, "`"+word+"`")
		}
	}
	require.Contains(t, string(RenderRPCPolicyMatrix()), rendered)
}
