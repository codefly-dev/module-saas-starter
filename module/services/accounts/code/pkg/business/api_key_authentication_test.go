package business_test

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// malformedScopes are permissions that cannot travel as one scope: the gateway
// joins a key's scopes with commas and every service splits them again, so
// users / read,*:* would come back as users:read and the root *:*.
var malformedScopes = map[string]*gen.Permission{
	"a comma in the action":    {Resource: "users", Action: "read,*:*"},
	"a comma in the resource":  {Resource: "*:*,users", Action: "read"},
	"a colon in the action":    {Resource: "users", Action: "read:x"},
	"whitespace in the action": {Resource: "users", Action: "read *"},
	"a control character":      {Resource: "users", Action: "read\x00"},
	"an empty action":          {Resource: "users"},
}

// A stored scope that cannot travel as one scope refuses its whole key rather
// than being dropped from it: a row written before CreateAPIKey checked the
// shape must not authenticate as a wider key.
func TestValidateAPIKeyRefusesAStoredScopeThatIsNotOneScope(t *testing.T) {
	stored := func(scopes ...*gen.Permission) *business.APIKeyAuthentication {
		return &business.APIKeyAuthentication{
			Key:    &gen.APIKey{UserId: "user", OrganizationId: "org", Scopes: scopes},
			Claims: business.APIKeyIdentityClaims{Member: true},
		}
	}
	usersRead := &gen.Permission{Resource: "users", Action: "read"}

	for name, malformed := range malformedScopes {
		t.Run(name, func(t *testing.T) {
			response, err := newAPIKeyAuthenticationService(t, stored(usersRead, malformed)).
				ValidateAPIKey(context.Background(), "presented-key")
			require.NoError(t, err)
			require.False(t, response.Valid)
			require.Empty(t, response.Scopes)
		})
	}

	response, err := newAPIKeyAuthenticationService(t, stored(usersRead, &gen.Permission{Resource: "*", Action: "*"})).
		ValidateAPIKey(context.Background(), "presented-key")
	require.NoError(t, err)
	require.True(t, response.Valid)
	require.Equal(t, []string{"users:read", "*:*"}, response.Scopes)
}

// apiKeyCreationStore counts the transactions CreateAPIKey opens and fails
// each, so a test sees whether a request got as far as the database.
type apiKeyCreationStore struct {
	business.Store
	transactions int
}

func (store *apiKeyCreationStore) WithOrgTx(context.Context, string, func(context.Context) error) error {
	store.transactions++
	return errors.New("stopped at the database")
}

// The domain refuses to mint a scope that cannot travel as one scope, whatever
// the transport in front of it checked.
func TestCreateAPIKeyRefusesAScopeThatIsNotOneScope(t *testing.T) {
	create := func(scopes ...*gen.Permission) (int, error) {
		store := &apiKeyCreationStore{}
		service, err := business.NewService(store)
		require.NoError(t, err)
		service.SetHasher(staticKeyHasher{})
		_, err = service.CreateAPIKey(context.Background(), "user", &gen.CreateAPIKeyRequest{
			OrganizationId: "org",
			Name:           "key",
			Scopes:         scopes,
		})
		return store.transactions, err
	}

	for name, malformed := range malformedScopes {
		t.Run(name, func(t *testing.T) {
			transactions, err := create(&gen.Permission{Resource: "users", Action: "read"}, malformed)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.Zero(t, transactions)
		})
	}

	transactions, err := create(&gen.Permission{Resource: "users", Action: "read"}, &gen.Permission{Resource: "*", Action: "*"})
	require.ErrorContains(t, err, "stopped at the database")
	require.Equal(t, 1, transactions)
}
