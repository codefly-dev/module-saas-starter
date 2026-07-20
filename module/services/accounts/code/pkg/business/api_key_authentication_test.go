package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

type apiKeyAuthenticationStore struct {
	business.Store
	authentication *business.APIKeyAuthentication
}

func (store *apiKeyAuthenticationStore) GetAPIKeyAuthentication(context.Context, string) (*business.APIKeyAuthentication, error) {
	return store.authentication, nil
}

type staticKeyHasher struct{}

func (staticKeyHasher) HashKey(context.Context, string) (string, error) { return "hash", nil }

func newAPIKeyAuthenticationService(t *testing.T, authentication *business.APIKeyAuthentication) *business.Service {
	t.Helper()
	service, err := business.NewService(&apiKeyAuthenticationStore{authentication: authentication})
	require.NoError(t, err)
	service.SetHasher(staticKeyHasher{})
	return service
}

func TestValidateAPIKeyRejectsOwnerWhoseMembershipWasRevoked(t *testing.T) {
	service := newAPIKeyAuthenticationService(t, &business.APIKeyAuthentication{
		Key: &gen.APIKey{UserId: "user", OrganizationId: "org"},
	})

	response, err := service.ValidateAPIKey(context.Background(), "presented-key")
	require.NoError(t, err)
	require.False(t, response.Valid)
}

func TestValidateAPIKeyProjectsCurrentMembershipAndPlatformRoles(t *testing.T) {
	service := newAPIKeyAuthenticationService(t, &business.APIKeyAuthentication{
		Key: &gen.APIKey{UserId: "user", OrganizationId: "org"},
		Claims: business.APIKeyIdentityClaims{
			Member:       true,
			OrgRole:      "admin",
			PlatformRole: "support",
			Roles:        []string{"developer"},
			Attributes:   map[string]string{"region": "us-east"},
		},
	})

	response, err := service.ValidateAPIKey(context.Background(), "presented-key")
	require.NoError(t, err)
	require.True(t, response.Valid)
	require.ElementsMatch(t, []string{"developer", "admin", "support"}, response.Roles)
	require.Equal(t, "admin", response.Attributes["org_role"])
	require.Equal(t, "support", response.Attributes["platform_role"])
}
