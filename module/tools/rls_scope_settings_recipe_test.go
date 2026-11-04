package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	scopeSettingsRecipe    = "rls-scope-settings-transition.sql"
	scopeSettingsMigration = "66_scope_settings_transition.up.sql"
)

// The recipe a consumer runs against its own database and the migration this
// module runs against its own are one text. A consumer's tenant policies are
// its own migrations' work, and until migration 65 the convention this
// repository documented was the host's `app.current_org_id`: upgrading the host
// without rewriting them leaves every one of its tables reading as empty, with
// no error. So the rewrite ships as a file a consumer can run, and the module's
// migration 66 embeds that same file rather than restating it.
//
// Two copies of a text-rewriting rule are two rules. This is what makes the one
// the migration applies the one the recipe hands out — and what the replay test
// in services/store/code proves, since it applies the migration.
func TestMigration66IsTheShippedScopeSettingsRecipe(t *testing.T) {
	recipe := readFile(t, filepath.Join("..", "tools", scopeSettingsRecipe))
	migration := readFile(t, filepath.Join("..", "services", "store", "migrations", scopeSettingsMigration))

	if !strings.Contains(migration, recipe) {
		t.Fatalf("%s does not contain %s byte for byte.\nThe migration is the recipe with a header comment: regenerate it with\n"+
			"  { printf '%%s\\n' \"<header>\"; cat module/tools/%s; } > module/services/store/migrations/%s",
			scopeSettingsMigration, scopeSettingsRecipe, scopeSettingsRecipe, scopeSettingsMigration)
	}
	// The migration adds a header and nothing else. A body appended AFTER the
	// recipe would still contain it, and would be a second rule in the one
	// place nobody would look for it.
	if tail := strings.TrimSpace(migration[strings.Index(migration, recipe)+len(recipe):]); tail != "" {
		t.Fatalf("%s has %d bytes of its own after the recipe; the migration may only add a header before it:\n%s",
			scopeSettingsMigration, len(tail), tail)
	}
	if head := strings.TrimSpace(migration[:strings.Index(migration, recipe)]); !strings.HasPrefix(head, "--") {
		t.Fatalf("%s must open with a comment header; it opens with %q", scopeSettingsMigration, firstLine(head))
	}
}

// The window is only a window if both halves of both names survive the rewrite,
// and if what it produces is still fail-closed. The replay test proves the
// behaviour against a real cluster; this is the cheap guard that the text has
// not lost one of the four names — a rewrite that silently stopped handling the
// user half would leave notifications, sessions and mfa_devices reading as
// empty for every caller on a pre-rename pod, which no SELECT in this
// repository's suite would notice.
func TestScopeSettingsRecipeCarriesBothGenerationsOfBothNames(t *testing.T) {
	recipe := readFile(t, filepath.Join("..", "tools", scopeSettingsRecipe))
	for _, setting := range []string{
		"codefly.current_tenant_id", "codefly.current_user_id",
		"app.current_org_id", "app.current_user_id",
	} {
		if !strings.Contains(recipe, setting) {
			t.Errorf("%s never names %s, so a predicate reading it is left outside the window", scopeSettingsRecipe, setting)
		}
	}
	// NULLIF is what keeps an empty binding from falling through to the other
	// generation's value: without it, a transaction that cleared the library's
	// half would be judged by whatever the retired half still held.
	if !strings.Contains(recipe, "NULLIF") || !strings.Contains(recipe, "COALESCE") {
		t.Errorf("%s must wrap the live read in COALESCE(NULLIF(...)): without NULLIF an empty binding falls through to the other name", scopeSettingsRecipe)
	}
	// An unbounded exclusive lock on every scoped relation is an outage with a
	// deploy's name on it.
	if !strings.Contains(recipe, "lock_timeout") {
		t.Errorf("%s takes an exclusive lock on every scoped relation and must bound the wait with lock_timeout", scopeSettingsRecipe)
	}
	// ALTER POLICY cannot restate a policy's kind, verb or role list, which is
	// the point: DROP and CREATE can get one of them wrong.
	if strings.Contains(recipe, "DROP POLICY") || !strings.Contains(recipe, "ALTER POLICY") {
		t.Errorf("%s must rewrite a predicate with ALTER POLICY, which cannot reassign the kind, verb or roles a DROP/CREATE pair restates", scopeSettingsRecipe)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
