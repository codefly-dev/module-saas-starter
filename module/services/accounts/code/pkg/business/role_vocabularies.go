package business

import (
	"slices"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
)

// RoleVocabulary is one of the three sets of role words on this host. Two of
// them share words (editor, viewer) and none stands in for another, so each
// says what it decides and where its words are declared. The matrix renders
// this table; nothing types the words a second time.
type RoleVocabulary struct {
	Name string
	// Words are read from the code that defines each vocabulary, never typed
	// here: the organisation roles from the OrgRole enum, the catalogue
	// built-ins from the roles the permission vocabulary grants, the
	// installation roles from the shared authority core's declaration rule.
	Words      []string
	Decides    string
	DeclaredIn string
}

// RoleVocabularies is the one table of the three role vocabularies, in the
// order a reader meets them: who administers the organisation, what a role
// assignment carries on this host's own resources, and what a person holds
// through one installation of a solution or a module page.
func RoleVocabularies() []RoleVocabulary {
	return []RoleVocabulary{
		{
			Name:  "Organisation role",
			Words: organizationRoleWords(),
			Decides: "Who administers the organisation. The `org_admin` tier admits `owner` and `admin` (and a platform `super_admin`); " +
				"`org_member` admits any member. The `/admin` pages gate on `roles:write`, which admits the same people " +
				"(`owner`, `admin`, a platform `super_admin`) as the API's org-admin check.",
			DeclaredIn: "the organisation membership; the session's `or` claim",
		},
		{
			Name:  "Catalogue built-in role",
			Words: catalogueBuiltInRoleWords(),
			Decides: "Which `<resource>:<action>` permissions (the Permission / scope column above) a role assignment carries " +
				"on this host's own resources.",
			DeclaredIn: "`servicePermissionVocabulary` in `pkg/business/service_vocabulary.go`",
		},
		{
			Name:  "Installation role",
			Words: installationRoleWords(),
			Decides: "What a person holds through one installation of a solution or of a module page that declares roles: " +
				"its page, its navigation entry, its Setup tab, and what a Work Context at its audience carries. " +
				"The shared authority core requires every solution, and every module page that declares roles, to declare " +
				"`installer`; `editor` and `viewer` are the standard an open handbook proposal sets, checked by nothing yet. " +
				"The installation's owner holds `installer`.",
			DeclaredIn: "the signed authority document of the solution or module page; granted per installation to a person or a team",
		},
	}
}

// organizationRoleWords are the OrgRole enum's names, highest first, as the
// session's `or` claim and the authorisation tiers spell them.
func organizationRoleWords() []string {
	var words []string
	for value := range gen.OrgRole_name {
		role := gen.OrgRole(value)
		if role == gen.OrgRole_ORG_ROLE_UNSPECIFIED {
			continue
		}
		words = append(words, strings.ToLower(strings.TrimPrefix(role.String(), "ORG_ROLE_")))
	}
	slices.SortFunc(words, func(a, b string) int {
		return int(gen.OrgRole_value["ORG_ROLE_"+strings.ToUpper(b)]) - int(gen.OrgRole_value["ORG_ROLE_"+strings.ToUpper(a)])
	})
	return words
}

// catalogueBuiltInRoleWords are the built-in roles the permission vocabulary
// grants at least one permission to, in the order the vocabulary first names
// them. A platform role that reaches a permission through the `*:*` root is
// not a catalogue role and is left out.
func catalogueBuiltInRoleWords() []string {
	var words []string
	for _, definition := range servicePermissionVocabulary {
		for _, holder := range definition.BuiltInRoles {
			name, _, _ := strings.Cut(holder, " (")
			if strings.HasPrefix(name, "platform ") || slices.Contains(words, name) {
				continue
			}
			words = append(words, name)
		}
	}
	return words
}

// installationRoleWords are the roles the shared authority core requires an
// installation target to declare: the installer alone, today; whatever else
// the target declares is its own.
func installationRoleWords() []string {
	return []string{domain.InstallerRole}
}

// renderRoleVocabularies is the matrix's "Three vocabularies" section, rendered
// from RoleVocabularies.
func renderRoleVocabularies() string {
	var b strings.Builder
	b.WriteString("\n## Three vocabularies\n\n")
	b.WriteString("Three sets of role words appear on this host. They answer different questions,\n")
	b.WriteString("and a word in one never means the same-spelled word in another.\n\n")
	b.WriteString("| Vocabulary | Words | What it decides | Declared in |\n|---|---|---|---|\n")
	for _, v := range RoleVocabularies() {
		words := make([]string, 0, len(v.Words))
		for _, w := range v.Words {
			words = append(words, "`"+w+"`")
		}
		spelled := strings.Join(words, ", ")
		if v.Name == "Installation role" {
			spelled += ", and whatever else a solution or a module page declares (the proposed standard adds `editor`, `viewer`)"
		}
		b.WriteString("| " + v.Name + " | " + spelled + " | " + escapeCell(v.Decides) + " | " + v.DeclaredIn + " |\n")
	}
	b.WriteString(`
The ` + "`/s/<id>`" + ` page, the solution navigation and the Setup tab speak only
installation roles. A module page (a module whose signed manifest declares a
frontend) may declare installation roles too: then they gate its page, its
navigation entry and its audience exactly as a solution's, and the
organisation's roles no longer apply there. A module that declares no roles,
which every module without a page is, keeps the organisation's roles at its
audience, and its page is reached through the organisation's installation of
it and a team grant at that installation. The shared authority core refuses
roles on a module without a page.
`)
	return b.String()
}
