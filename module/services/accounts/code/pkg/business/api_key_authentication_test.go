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

// hashKeyedStore answers only for the one hash a key is actually stored under,
// which is what makes the lookup's single-candidate assumption visible: the
// store is keyed BY hash, so a hash computed under a different key finds nothing
// and is indistinguishable from a revoked key.
type hashKeyedStore struct {
	business.Store
	storedUnder    string
	authentication *business.APIKeyAuthentication
	lookups        []string
}

func (store *hashKeyedStore) GetAPIKeyAuthentication(_ context.Context, keyHash string) (*business.APIKeyAuthentication, error) {
	store.lookups = append(store.lookups, keyHash)
	if keyHash != store.storedUnder {
		return nil, nil
	}
	return store.authentication, nil
}

// cutoverHasher is a key service mid-cutover: it hashes new keys under the
// selected backend and can also produce the hash the outgoing backend wrote.
type cutoverHasher struct{ selected, previous string }

func (h cutoverHasher) HashKey(context.Context, string) (string, error) { return h.selected, nil }

func (h cutoverHasher) CandidateHashes(context.Context, string) ([]string, error) {
	return []string{h.selected, h.previous}, nil
}

// A keyed hash cannot be re-keyed — the plaintext is gone once the key is
// issued — so the re-seal sweep that rewrites every enveloped column cannot
// touch api_keys.key_hash. If the lookup used only the selected backend's hash,
// every API key issued before a key-service cutover would stop authenticating,
// reported as simply invalid.
func TestValidateAPIKeyFindsAKeyStoredUnderThePreviousBackend(t *testing.T) {
	store := &hashKeyedStore{
		storedUnder: "hash-written-by-the-outgoing-backend",
		authentication: &business.APIKeyAuthentication{
			Key:    &gen.APIKey{UserId: "user", OrganizationId: "org"},
			Claims: business.APIKeyIdentityClaims{Member: true, OrgRole: "admin"},
		},
	}
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetHasher(cutoverHasher{
		selected: "hash-under-the-selected-backend",
		previous: "hash-written-by-the-outgoing-backend",
	})

	response, err := service.ValidateAPIKey(context.Background(), "presented-key")
	require.NoError(t, err)
	require.True(t, response.Valid,
		"a key issued before the cutover must still authenticate while the previous backend is bound")
	require.Equal(t,
		[]string{"hash-under-the-selected-backend", "hash-written-by-the-outgoing-backend"},
		store.lookups,
		"the selected backend's hash is tried first, so a settled deployment does one lookup")
}

// Once the cutover is finished the previous backend is withdrawn and there is a
// single hash to look up: no extra query per validation.
func TestValidateAPIKeyDoesOneLookupWhenNoCutoverIsInProgress(t *testing.T) {
	store := &hashKeyedStore{
		storedUnder: "hash",
		authentication: &business.APIKeyAuthentication{
			Key:    &gen.APIKey{UserId: "user", OrganizationId: "org"},
			Claims: business.APIKeyIdentityClaims{Member: true, OrgRole: "admin"},
		},
	}
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetHasher(staticKeyHasher{})

	response, err := service.ValidateAPIKey(context.Background(), "presented-key")
	require.NoError(t, err)
	require.True(t, response.Valid)
	require.Equal(t, []string{"hash"}, store.lookups)
}
