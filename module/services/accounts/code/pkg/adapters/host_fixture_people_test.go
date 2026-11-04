package adapters_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"accounts/pkg/adapters"
	"accounts/pkg/auth"
	"accounts/pkg/business"
	sessionv1 "accounts/pkg/gen/host/session/v1"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type fixtureListingStore struct {
	business.Store
	organization *gen.Organization
	readError    error
	readScope    string
}

func (s *fixtureListingStore) WithOrgTx(ctx context.Context, org string, fn func(context.Context) error) error {
	s.readScope = org
	return fn(ctx)
}

func (s *fixtureListingStore) GetOrganization(_ context.Context, org string) (*gen.Organization, error) {
	if s.readScope != org {
		return nil, errors.New("organization read outside its scope")
	}
	return s.organization, s.readError
}

func (*fixtureListingStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*fixtureListingStore) ListTeams(context.Context, string, string) ([]*gen.Team, error) {
	return nil, nil
}

type fixtureListingIdentity struct{ identity *auth.Identity }

func (*fixtureListingIdentity) FixtureTokens() []string { return []string{"fixture-person"} }
func (*fixtureListingIdentity) Validate(context.Context, string) (*auth.Claims, error) {
	return &auth.Claims{Provider: "dev", Subject: "fixture-person"}, nil
}
func (r *fixtureListingIdentity) LookupIdentity(context.Context, *auth.Claims) (*auth.Identity, error) {
	return r.identity, nil
}
func (*fixtureListingIdentity) Resolve(context.Context, *auth.Claims, auth.Intent) (*auth.Identity, error) {
	return nil, errors.New("fixture listing must not sign in")
}

// Exercise the real business projection and generated wire adapter without
// booting a database, a provider, or a service runtime.
func TestHostFixturePeopleCarryCurrentOrganizationNamesOnTheWire(t *testing.T) {
	org := uuid.New()
	store := &fixtureListingStore{organization: &gen.Organization{Id: org.String(), Name: "Example organization"}}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	identity := &fixtureListingIdentity{identity: &auth.Identity{UserID: uuid.New(), OrgID: org, OrgRole: "member", Email: "person@example.com"}}
	svc.SetDevelopmentTokenValidator(identity)
	svc.SetIdentityResolver(identity)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	sessionv1.RegisterSessionServiceServer(server, adapters.NewHostSessionServer(svc))
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///fixture-session",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := sessionv1.NewSessionServiceClient(conn)
	for _, name := range []string{"Example organization", "Renamed organization"} {
		store.organization.Name = name
		out, err := client.ListFixturePeople(t.Context(), &sessionv1.ListFixturePeopleRequest{})
		require.NoError(t, err)
		require.Len(t, out.GetPeople(), 1)
		require.Equal(t, org.String(), out.People[0].GetOrganizationId())
		require.Equal(t, name, out.People[0].GetOrganizationDisplayName())
		require.Equal(t, org.String(), store.readScope)
	}
	store.readError = errors.New("organization unavailable")
	out, err := client.ListFixturePeople(t.Context(), &sessionv1.ListFixturePeopleRequest{})
	require.Equal(t, codes.Internal, status.Code(err))
	require.Nil(t, out)
	identity.identity.OrgID = uuid.Nil
	out, err = client.ListFixturePeople(t.Context(), &sessionv1.ListFixturePeopleRequest{})
	require.NoError(t, err)
	require.Empty(t, out.People[0].GetOrganizationDisplayName())
	svc.SetDevelopmentTokenValidator(nil)
	out, err = client.ListFixturePeople(t.Context(), &sessionv1.ListFixturePeopleRequest{})
	require.NoError(t, err)
	require.Empty(t, out.GetPeople())
}
