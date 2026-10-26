package business

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"accounts/pkg/auth"
)

// FixturePerson is one person the development fixture provider signs in, as
// this host knows them: the canonical person, their organisation, their role
// there and their teams, and the token SignIn takes for them.
type FixturePerson struct {
	PersonID       string
	Email          string
	OrganizationID string
	Roles          []string
	TeamIDs        []string
	FixtureToken   string
}

// fixtureAllowlist is the development validator's ordered allowlist.
type fixtureAllowlist interface {
	FixtureTokens() []string
}

// identityLookup projects an existing person's identity with login's
// organisation selection and none of login's effects.
type identityLookup interface {
	LookupIdentity(ctx context.Context, claims *auth.Claims) (*auth.Identity, error)
}

// ListFixturePeople answers who fixture sign-in admits, in the fixture's order.
//
// The gate is the one Authenticate keeps: with no development validator wired
// (any deployment that signs real people in), there are no fixture people and
// the answer is empty, never a refusal and never a read of a fixture file. A
// listed person is projected through the identity resolver's read-only lookup,
// so the organisation is the one their sign-in lands in; a token whose person is
// not yet seeded is omitted, because it names nobody this host can describe.
// Listing opens no session and touches no login state.
func (s *Service) ListFixturePeople(ctx context.Context) ([]FixturePerson, error) {
	if s.devValidator == nil {
		return nil, nil
	}
	allowlist, ok := s.devValidator.(fixtureAllowlist)
	if !ok {
		return nil, errors.New("the development validator does not enumerate its fixture")
	}
	lookup, ok := s.resolver.(identityLookup)
	if !ok {
		return nil, errors.New("the identity resolver has no read-only lookup")
	}
	var people []FixturePerson
	for _, token := range allowlist.FixtureTokens() {
		claims, err := s.devValidator.Validate(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("fixture token %q: %w", token, err)
		}
		identity, err := lookup.LookupIdentity(ctx, claims)
		if errors.Is(err, auth.ErrNoAccount) {
			continue
		}
		if err != nil {
			return nil, err
		}
		person := FixturePerson{PersonID: identity.UserID.String(), Email: identity.Email, FixtureToken: token}
		if identity.OrgID != uuid.Nil {
			person.OrganizationID = identity.OrgID.String()
			person.Roles = []string{identity.OrgRole}
			if person.TeamIDs, err = s.fixtureTeamIDs(ctx, person.OrganizationID, person.PersonID); err != nil {
				return nil, err
			}
		}
		people = append(people, person)
	}
	return people, nil
}

// fixtureTeamIDs is the teams one fixture person belongs to. The listing has no
// caller identity to scope a tenant read by (the sign-in page asks before anyone
// signs in), so it reads as the control plane; it is reached only with the
// development validator wired, and reads nothing but that person's teams.
func (s *Service) fixtureTeamIDs(ctx context.Context, orgID, userID string) ([]string, error) {
	var ids []string
	err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		teams, err := s.store.ListTeams(ctx, orgID, userID)
		for _, team := range teams {
			ids = append(ids, team.GetId())
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("teams of %s in %s: %w", userID, orgID, err)
	}
	return ids, nil
}
