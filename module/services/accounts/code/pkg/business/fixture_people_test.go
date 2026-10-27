package business_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// fixtureValidator is an enabled development allowlist, in fixture order.
type fixtureValidator struct{ tokens []string }

func (v fixtureValidator) FixtureTokens() []string { return v.tokens }
func (v fixtureValidator) Validate(_ context.Context, token string) (*auth.Claims, error) {
	return &auth.Claims{Provider: "dev", Subject: token, Email: token + "@example.com"}, nil
}

// lookupResolver projects seeded people only; it counts what a login would do.
type lookupResolver struct {
	people  map[string]*auth.Identity
	resolve int
}

func (r *lookupResolver) Resolve(context.Context, *auth.Claims, auth.Intent) (*auth.Identity, error) {
	r.resolve++
	return nil, auth.ErrNoAccount
}
func (r *lookupResolver) LookupIdentity(_ context.Context, c *auth.Claims) (*auth.Identity, error) {
	if person, ok := r.people[c.Subject]; ok {
		return person, nil
	}
	return nil, auth.ErrNoAccount
}

// teamStore answers the teams of one member, as the control plane.
type teamStore struct {
	business.Store
	teams map[string][]*gen.Team
}

func (s teamStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s teamStore) ListTeams(_ context.Context, orgID, memberID string) ([]*gen.Team, error) {
	return s.teams[orgID+"/"+memberID], nil
}

func TestFixturePeopleAreTheEnabledAllowlistInOrderAndNothingElse(t *testing.T) {
	org, viewer, orgless := uuid.New(), uuid.New(), uuid.New()
	store := teamStore{teams: map[string][]*gen.Team{org.String() + "/" + viewer.String(): {{Id: "team-1"}, {Id: "team-2"}}}}
	svc, err := business.NewService(store)
	require.NoError(t, err)

	people, err := svc.ListFixturePeople(t.Context())
	require.NoError(t, err)
	require.Empty(t, people, "no development validator: no fixture people")

	resolver := &lookupResolver{people: map[string]*auth.Identity{
		"fixture-viewer":  {UserID: viewer, OrgID: org, OrgRole: "member", Email: "viewer@example.com"},
		"fixture-orgless": {UserID: orgless, Email: "orgless@example.com"},
	}}
	svc.SetIdentityResolver(resolver)
	svc.SetDevelopmentTokenValidator(fixtureValidator{tokens: []string{"fixture-orgless", "fixture-unseeded", "fixture-viewer"}})
	people, err = svc.ListFixturePeople(t.Context())
	require.NoError(t, err)
	require.Equal(t, []business.FixturePerson{
		{PersonID: orgless.String(), Email: "orgless@example.com", FixtureToken: "fixture-orgless"},
		{PersonID: viewer.String(), Email: "viewer@example.com", OrganizationID: org.String(), Roles: []string{"member"},
			TeamIDs: []string{"team-1", "team-2"}, FixtureToken: "fixture-viewer"},
	}, people, "fixture order, the unseeded token omitted")
	require.Zero(t, resolver.resolve, "listing is never a login")
}
