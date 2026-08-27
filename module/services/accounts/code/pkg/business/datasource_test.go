package business

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidGitHubRepo(t *testing.T) {
	valid := []string{"octocat/hello-world", "a/b", "org.name/repo.name", "under_score/dash-repo"}
	for _, r := range valid {
		require.True(t, validGitHubRepo(r), "%q should be valid", r)
	}
	invalid := []string{
		"", "octocat", "/hello", "octocat/", "a/b/c", "octo cat/repo", "octocat/re po",
		// Out-of-charset / URL metacharacters that must not reach a GitHub API URL.
		"octocat/hello?ref=x", "octocat/hello#frag", "octocat/hel lo", "octo/cat/repo",
		// Path-traversal segment names GitHub itself forbids.
		"../etc", "octocat/..", "./x", "x/.",
		// Over-length segments (owner > 39, repo > 100).
		strings.Repeat("a", 40) + "/repo", "owner/" + strings.Repeat("b", 101),
	}
	for _, r := range invalid {
		require.False(t, validGitHubRepo(r), "%q should be invalid", r)
	}
}

func TestNormalizePaths(t *testing.T) {
	require.Equal(t, []string{"docs", "src/lib"}, normalizePaths([]string{" docs ", "", "src/lib", "  "}))
	// Nothing survives, but the result is non-nil so the NOT NULL column keeps
	// its empty-array default.
	got := normalizePaths([]string{"", "   "})
	require.NotNil(t, got)
	require.Empty(t, got)
	require.NotNil(t, normalizePaths(nil))
}

func TestDatasourceSecretPurposeBindsToID(t *testing.T) {
	require.Equal(t, "datasource:abc", DatasourceSecretPurpose("abc"))
	require.NotEqual(t, DatasourceSecretPurpose("a"), DatasourceSecretPurpose("b"))
}
