package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	"accounts/pkg/datasource/connector"
)

// How a source authenticates is read from what the host holds for it — the
// installation it is bound to, the public marker, or a sealed credential —
// never from the credential itself. The Data sources page shows it so a person
// knows a token is saved without ever seeing the token.
func TestCredentialMode_SaysHowEachSourceAuthenticates(t *testing.T) {
	for _, c := range []struct {
		name   string
		source business.DatasourceSource
		want   connector.CredentialMode
	}{
		{"a repository read with a token", business.DatasourceSource{Provider: business.DatasourceProviderGitHub, CredentialSecretRef: "enc"}, connector.CredentialStaticSecret},
		{"a repository read through the app", business.DatasourceSource{Provider: business.DatasourceProviderGitHub, CredentialSecretRef: "enc", GitHubInstallationID: "42"}, connector.CredentialOrgApp},
		{"a public repository", business.DatasourceSource{Provider: business.DatasourceProviderGitHub, GitHubCredentialKind: "public"}, connector.CredentialNone},
		{"a bucket", business.DatasourceSource{Provider: business.DatasourceProviderUpload, CredentialSecretRef: "enc"}, connector.CredentialStaticSecret},
		{"a site read without a credential", business.DatasourceSource{Provider: business.DatasourceProviderCrawler}, connector.CredentialNone},
		{"a person's own api source", business.DatasourceSource{Provider: business.DatasourceProviderAPI, CredentialSecretRef: "enc", PersonalOwnerUserID: "user-1"}, connector.CredentialUserOAuth},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, c.source.CredentialMode())
		})
	}
}

// A token given to a repository GitHub serves to anyone can be taken back: the
// source keeps syncing as a public repository, and the sealed token is gone
// from its row.
func TestRemoveCredential_ATokenSourceOfAPublicRepositoryBecomesPublic(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{defaultBranch: "main", commit: "HEAD", public: true})
	source := h.publicSource(t)
	_, err := h.svc.ReconnectDatasourceSource(context.Background(), "actor-1", testOrg, source.ID, "ghp_new")
	require.NoError(t, err)
	stored := h.stored(t, source.ID)
	require.Equal(t, connector.CredentialStaticSecret, stored.CredentialMode())

	jobID, err := h.svc.RemoveDatasourceSourceCredential(context.Background(), "actor-1", testOrg, source.ID)
	require.NoError(t, err)
	require.NotEmpty(t, jobID, "the source syncs right away without its token")

	stored = h.stored(t, source.ID)
	require.Empty(t, stored.CredentialSecretRef, "the sealed token is deleted")
	require.Equal(t, "public", stored.GitHubCredentialKind)
	require.Equal(t, connector.CredentialNone, stored.CredentialMode())
	requireDeclaredPayloads(t, h.audit)
}

// Removing the token of a repository GitHub serves only with one would stop the
// source dead, so it is refused and the token stays.
func TestRemoveCredential_RefusedWhenGitHubServesTheRepositoryOnlyWithAToken(t *testing.T) {
	gh := &fakeGitHub{defaultBranch: "main", commit: "HEAD", public: true}
	h := newPublicHarness(t, gh)
	source := h.publicSource(t)
	_, err := h.svc.ReconnectDatasourceSource(context.Background(), "actor-1", testOrg, source.ID, "ghp_new")
	require.NoError(t, err)
	gh.public = false

	_, err = h.svc.RemoveDatasourceSourceCredential(context.Background(), "actor-1", testOrg, source.ID)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "err = %v", err)
	require.Contains(t, status.Convert(err).Message(), "Replace it")
	require.NotEmpty(t, h.stored(t, source.ID).CredentialSecretRef, "the token stays")
}

// A source that holds no token has none to remove.
func TestRemoveCredential_RefusedForASourceThatHoldsNoToken(t *testing.T) {
	h := newPublicHarness(t, &fakeGitHub{defaultBranch: "main", commit: "HEAD", public: true})
	source := h.publicSource(t)

	_, err := h.svc.RemoveDatasourceSourceCredential(context.Background(), "actor-1", testOrg, source.ID)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "err = %v", err)
}
