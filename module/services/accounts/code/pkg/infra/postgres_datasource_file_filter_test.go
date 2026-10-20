//go:build !pure

package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// Exercise real persistence, not a config JSON round-trip in memory. The same
// source is reloaded by the tenant reader and the background compiler.
func TestPostgresDatasourceFileFilterPersistsAcrossReaders(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	otherOrg := seedOrg(t, owner)
	legacyID := seedDatasourceSource(t, org)
	legacy := installationSourceByID(t, legacyID)
	require.Empty(t, legacy.FileExtensions, "legacy NULL config must mean all types")

	source := *legacy
	source.ID = business.NewIDString()
	source.FileExtensions = []string{".md", ".mdx"}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.InsertDatasourceSource(ctx, &source)
	}))
	// A separate transaction must reconstruct the persisted filter.
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		got, err := testStore.GetDatasourceSource(ctx, org, source.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, source.FileExtensions, got.FileExtensions)
		all, err := testStore.ListDatasourceSources(ctx, org)
		require.NoError(t, err)
		found := false
		for _, item := range all {
			if item.ID == source.ID {
				found = true
				require.Equal(t, source.FileExtensions, item.FileExtensions)
			}
		}
		require.True(t, found)
		return nil
	}))
	require.Equal(t, source.FileExtensions, installationSourceByID(t, source.ID).FileExtensions)
	require.NoError(t, testStore.WithOrgTx(testCtx, otherOrg, func(ctx context.Context) error {
		got, err := testStore.GetDatasourceSource(ctx, otherOrg, source.ID)
		require.NoError(t, err)
		require.Nil(t, got, "the filter does not relax source tenant isolation")
		return nil
	}))
}

// A public source's credential-less record survives a reload by both readers,
// and writing an envelope drops it in the same statement — so a source that is
// later given a PAT or moved onto the App is never read as both.
func TestPostgresDatasourcePublicCredentialKindPersistsAndClearsWithAnEnvelope(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	template := installationSourceByID(t, seedDatasourceSource(t, org))

	source := *template
	source.ID = business.NewIDString()
	source.CredentialSecretRef = ""
	source.GitHubInstallationID = ""
	source.GitHubCredentialKind = "public"
	source.FileExtensions = []string{".md"}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.InsertDatasourceSource(ctx, &source)
	}))

	reloaded := installationSourceByID(t, source.ID)
	require.Equal(t, "public", reloaded.GitHubCredentialKind)
	require.Empty(t, reloaded.CredentialSecretRef)
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		got, err := testStore.GetDatasourceSource(ctx, org, source.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "public", got.GitHubCredentialKind)
		return nil
	}))

	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.UpdateDatasourceSourceCredential(ctx, org, source.ID, "vault:v1:envelope")
	}))
	updated := installationSourceByID(t, source.ID)
	require.Empty(t, updated.GitHubCredentialKind, "an envelope write must drop the credential-less record")
	require.Equal(t, "vault:v1:envelope", updated.CredentialSecretRef)
	require.Equal(t, []string{".md"}, updated.FileExtensions, "the rest of the config is untouched")
}

// The fetch-scope update writes the three fields it owns and nothing else — and
// in particular rebuilds the github config envelope from the whole source, not
// from the file filter alone. Rebuilt from the filter, a public source's
// credential-less record would be dropped by every edit, and from then on every
// fetch would look for an envelope that was never stored.
func TestPostgresUpdateDatasourceSourceFetchScopeKeepsEverythingElse(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	template := installationSourceByID(t, seedDatasourceSource(t, org))

	source := *template
	source.ID = business.NewIDString()
	source.CredentialSecretRef = ""
	source.GitHubInstallationID = ""
	source.GitHubCredentialKind = "public"
	source.Branch = "main"
	source.Paths = []string{"docs"}
	source.FileExtensions = []string{".md"}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.InsertDatasourceSource(ctx, &source)
	}))

	moved := source
	moved.Branch = "release"
	moved.Paths = []string{"docs", "guides"}
	moved.FileExtensions = []string{".md", ".mdx"}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.UpdateDatasourceSourceFetchScope(ctx, &moved)
	}))

	reloaded := installationSourceByID(t, source.ID)
	require.Equal(t, "release", reloaded.Branch)
	require.Equal(t, []string{"docs", "guides"}, reloaded.Paths)
	require.Equal(t, []string{".md", ".mdx"}, reloaded.FileExtensions)
	require.Equal(t, "public", reloaded.GitHubCredentialKind,
		"the config envelope must be rebuilt from the whole source, not from the file filter")
	require.Equal(t, template.Repo, reloaded.Repo, "an edit cannot re-aim the source at another repository")
	require.Equal(t, source.BoundaryNodeID, reloaded.BoundaryNodeID, "an edit cannot move the collection")
	require.Empty(t, reloaded.CredentialSecretRef)

	// Clearing is what an empty field means, exactly as on connect.
	cleared := moved
	cleared.Branch = ""
	cleared.Paths = nil
	cleared.FileExtensions = nil
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.UpdateDatasourceSourceFetchScope(ctx, &cleared)
	}))
	empty := installationSourceByID(t, source.ID)
	require.Empty(t, empty.Branch)
	require.Empty(t, empty.Paths)
	require.Empty(t, empty.FileExtensions)
}

// The statement's own answer to "was it there?". UpdateSource reads the source
// to validate against in one transaction and writes it in another, so under
// READ COMMITTED the row can be deleted in between and the UPDATE simply
// matches nothing. Reported as not-found, because the caller commits an audit
// record and queues a reconcile on the strength of this returning nil.
func TestPostgresUpdateDatasourceSourceFetchScopeReportsAMissingRow(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	source := installationSourceByID(t, seedDatasourceSource(t, org))

	gone := *source
	gone.ID = business.NewIDString()
	gone.Branch = "release"
	err := testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.UpdateDatasourceSourceFetchScope(ctx, &gone)
	})
	require.ErrorIs(t, err, business.ErrDatasourceSourceNotFound,
		"an UPDATE that matched no row must not read as a successful edit")

	// Another organization's id is equally no row of this one's, and the tenant
	// policy is what makes it so — the same empty result, not a silent write.
	foreign := *source
	foreign.OrgID = seedOrg(t, seedUser(t))
	foreign.Branch = "release"
	err = testStore.WithOrgTx(testCtx, foreign.OrgID, func(ctx context.Context) error {
		return testStore.UpdateDatasourceSourceFetchScope(ctx, &foreign)
	})
	require.ErrorIs(t, err, business.ErrDatasourceSourceNotFound)

	reloaded := installationSourceByID(t, source.ID)
	require.Equal(t, source.Branch, reloaded.Branch, "neither refusal may have written")
}

// The locked read is what an edit compares its expectation against, so it has
// to return the row as stored — including the file-type filter that lives in the
// config envelope rather than a column of its own — and nothing for a row that
// is not there.
func TestPostgresLockDatasourceSourceFetchScopeReadsTheStoredScope(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	template := installationSourceByID(t, seedDatasourceSource(t, org))

	source := *template
	source.ID = business.NewIDString()
	source.Branch = "main"
	source.Paths = []string{"docs"}
	source.FileExtensions = []string{".md"}
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		return testStore.InsertDatasourceSource(ctx, &source)
	}))

	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		locked, err := testStore.LockDatasourceSourceFetchScope(ctx, org, source.ID)
		if err != nil {
			return err
		}
		require.NotNil(t, locked)
		require.Equal(t, "main", locked.Branch)
		require.Equal(t, []string{"docs"}, locked.Paths)
		require.Equal(t, []string{".md"}, locked.FileExtensions)

		missing, err := testStore.LockDatasourceSourceFetchScope(ctx, org, business.NewIDString())
		if err != nil {
			return err
		}
		require.Nil(t, missing, "a row that is not there must read as absent, not as an empty scope")
		return nil
	}))
}
