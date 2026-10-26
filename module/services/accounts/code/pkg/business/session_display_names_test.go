package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// displayNamesStore is the store surface SessionDisplayNames reads: the person
// as themself and the organization as itself, each recorded so the test can
// hold the read to its own row-level binding.
type displayNamesStore struct {
	Store
	user    *gen.User
	org     *gen.Organization
	asUser  string
	asOrgTx string
}

func (s *displayNamesStore) As(id Identity) Scoped {
	s.asUser = id.UserID
	return &principalLookupScope{}
}

func (s *displayNamesStore) GetUser(context.Context, string) (*gen.User, error) { return s.user, nil }

func (s *displayNamesStore) WithOrgTx(ctx context.Context, orgID string, fn func(context.Context) error) error {
	s.asOrgTx = orgID
	return fn(ctx)
}

func (s *displayNamesStore) GetOrganization(context.Context, string) (*gen.Organization, error) {
	return s.org, nil
}

// A session names its person by their profile name, or their primary email
// when they gave none, and its organization by the organization's name; each
// read is bound to the row it reads.
func TestSessionDisplayNamesAreThePersonsAndTheOrganizations(t *testing.T) {
	const person, org = "00000000-0000-7000-8000-0000000000a1", "00000000-0000-7000-8000-0000000000c1"
	for name, tc := range map[string]struct {
		profile map[string]string
		want    string
	}{
		"profile name":       {map[string]string{"name": "  Bob Williams "}, "Bob Williams"},
		"no name, the email": {map[string]string{"name": " "}, "bob@acme.test"},
	} {
		t.Run(name, func(t *testing.T) {
			store := &displayNamesStore{
				user: &gen.User{Uuid: person, PrimaryEmail: "bob@acme.test", Profile: tc.profile},
				org:  &gen.Organization{Id: org, Name: "Acme Corp"},
			}
			svc, err := NewService(store)
			require.NoError(t, err)
			gotPerson, gotOrg, err := svc.SessionDisplayNames(context.Background(), person, org)
			require.NoError(t, err)
			require.Equal(t, tc.want, gotPerson)
			require.Equal(t, "Acme Corp", gotOrg)
			require.Equal(t, person, store.asUser, "the person is read as themself")
			require.Equal(t, org, store.asOrgTx, "the organization is read bound to itself")
		})
	}
}

// A session signed into no organization names its person and no organization,
// and reads no organization to do it.
func TestSessionDisplayNamesInNoOrganizationNameThePersonAlone(t *testing.T) {
	store := &displayNamesStore{user: &gen.User{PrimaryEmail: "bob@acme.test", Profile: map[string]string{"name": "Bob"}}}
	svc, err := NewService(store)
	require.NoError(t, err)
	person, org, err := svc.SessionDisplayNames(context.Background(), "00000000-0000-7000-8000-0000000000a1", "")
	require.NoError(t, err)
	require.Equal(t, "Bob", person)
	require.Empty(t, org)
	require.Empty(t, store.asOrgTx, "no organization is read")
}
