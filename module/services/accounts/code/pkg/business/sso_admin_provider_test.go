package business

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// ssoProviderStore is a partial fake carrying only the SSO row and the tx
// wrapper the setup path uses.
type ssoProviderStore struct {
	Store
	cfg *OrgSSOConfig
}

func (f *ssoProviderStore) GetOrgSSO(context.Context, string) (*OrgSSOConfig, error) {
	return f.cfg, nil
}

func (f *ssoProviderStore) UpsertOrgSSO(_ context.Context, cfg *OrgSSOConfig) error {
	stored := *cfg
	f.cfg = &stored
	return nil
}

func (f *ssoProviderStore) WithOrgTx(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// TestStartSSOSetup_ProviderOrganizationSurvivesALaterFailure covers the
// compensation gap between the two provider calls. Creating the organization is
// an external write nothing here can undo, so if its identifier is only
// persisted after the portal link succeeds, a portal failure strands it: the
// retry reads no configuration and mints a second organization, and every
// subsequent failure mints another.
func TestStartSSOSetup_ProviderOrganizationSurvivesALaterFailure(t *testing.T) {
	organizationCalls := 0
	portalShouldFail := true

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/organizations":
			organizationCalls++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"org_provider_1"}`))
		case "/portal/generate_link":
			if portalShouldFail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"link":"https://provider.example/portal/session"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()

	previousBase := workosBase
	workosBase = provider.URL
	defer func() { workosBase = previousBase }()

	store := &ssoProviderStore{}
	service, err := NewService(store)
	require.NoError(t, err)
	service.SetSSOManagementAPIKey("test-management-key")

	_, err = service.StartSSOSetup(context.Background(), "actor-1", "org-1", "https://app.example/admin/sso")
	require.Error(t, err, "a failed portal link must fail the call")
	require.Equal(t, 1, organizationCalls)
	require.NotNil(t, store.cfg, "the provider organization id must be persisted before the portal step")
	require.Equal(t, "org_provider_1", store.cfg.OrganizationID)
	require.Empty(t, store.cfg.Status, "setup is not linked until a portal session is minted")

	// The retry reuses the organization already created rather than minting a
	// second one that nothing will ever reference.
	portalShouldFail = false
	link, err := service.StartSSOSetup(context.Background(), "actor-1", "org-1", "https://app.example/admin/sso")
	require.NoError(t, err)
	require.Equal(t, "https://provider.example/portal/session", link)
	require.Equal(t, 1, organizationCalls, "a retry must not create a second provider organization")
	require.Equal(t, "linked", store.cfg.Status)
	require.Equal(t, "org_provider_1", store.cfg.OrganizationID)
}
