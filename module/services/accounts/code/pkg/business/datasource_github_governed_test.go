package business_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// A source a solution governs, connected by a member who does not administer
// the organization, never reads through the organization's GitHub App: an
// editor with no access to a private repository the App covers would
// otherwise ingest it into the solution's collection. With no credential of
// their own the connect is refused before the App is asked for anything.
func TestAGovernedConnectByAMemberNeverFallsBackToTheOrganizationsApp(t *testing.T) {
	h := newAppHarness(t, &fakeGitHubApp{})
	h.claimInstallation(t, testOrg)
	input := business.AddGitHubSourceInput{OrgID: testOrg, Repo: "acme/hr-private", CollectionLabel: "hr",
		Governance: &business.SourceGovernance{InstallationID: "inst-1", Kind: "example.page", Action: "write"}}

	_, err := h.svc.AddGitHubSource(context.Background(), "editor-1", input)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "err = %v", err)
	require.Contains(t, status.Convert(err).Message(), "with their own repository-scoped token")
	h.app.mu.Lock()
	mints := h.app.mints
	h.app.mu.Unlock()
	require.Zero(t, mints, "the organization's App was asked for a token")

	generic := business.AddSourceInput{OrgID: testOrg, Provider: business.DatasourceProviderGitHub, Repo: "acme/hr-private", CollectionLabel: "hr",
		Governance: input.Governance}
	_, err = h.svc.AddSource(context.Background(), "editor-1", generic)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "the provider-agnostic connect is held to the same rule: %v", err)
}
