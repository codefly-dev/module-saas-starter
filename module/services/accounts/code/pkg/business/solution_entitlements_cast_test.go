//go:build pure

package business

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// The entitlement listing is read for a session, not for a person: a session
// cast onto a team (OpenTeamCast) lists what the team holds and nothing of the
// person's own — the pages their direct grants and other teams reach are not
// theirs in that session. Both halves of the listing narrow the same way: the
// core's, by reading the person under the session's view, and this host's own
// grant half, by being told the team the view amounts to.
//
// Red before the session reached the listing: the gateway asked about the
// person alone, so a cast listed every page its person held and none it did
// not — and the dashboard of a cast showed nothing of the team's.
func TestSolutionEntitlements_ACastIsReadAsItsTeam(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	store.PutOrganization(domain.Organization{ID: "acme", Name: "Acme"})
	store.PutTeam("acme", "finance")
	store.PutTeam("acme", "legal")
	// tam belongs to legal and holds viewer on "own" directly; finance holds
	// viewer on "team", and tam is not in it.
	store.PutPerson(domain.Person{ID: "tam", Organization: "acme", Teams: []string{"legal"}})
	store.PutPerson(domain.Person{ID: "ines", Organization: "acme", Teams: []string{"finance"}})
	require.NoError(t, store.Write(ctx, func(tx authority.Tx) error {
		for _, inst := range []string{"own", "legal", "team"} {
			if err := tx.PutInstallation(domain.Installation{ID: "inst-" + inst, Organization: "acme", Target: inst, Status: domain.Active, Revision: 1}); err != nil {
				return err
			}
		}
		for _, g := range []domain.RoleGrant{
			{InstallationID: "inst-own", Role: "viewer", PersonID: "tam"},
			{InstallationID: "inst-legal", Role: "viewer", TeamID: "legal"},
			{InstallationID: "inst-team", Role: "viewer", TeamID: "finance"},
		} {
			if err := tx.PutRoleGrant(g); err != nil {
				return err
			}
		}
		return nil
	}))
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := authority.New(store, authority.Config{Issuer: "test.example", KeyID: "key", Signer: private,
		Keys: staticKeys{"key": public}, Replay: workcontext.NewMemoryReplayStore()})
	require.NoError(t, err)
	s := &Service{}
	s.authorityCore = &authorityCoreState{core: core}

	held := func(who authority.Person) []string {
		t.Helper()
		out, err := core.HeldInstallations(ctx, who, "acme")
		require.NoError(t, err)
		var ids []string
		for _, h := range out {
			ids = append(ids, h.Installation.ID)
		}
		return ids
	}

	// ines, a member of finance, casts onto it: the cast is a session of hers.
	_, err = core.OpenTeamCast(ctx, authority.Person{ID: "ines", Organization: "acme", SessionID: "ines-session"},
		authority.TeamCastRequest{Organization: "acme", Team: "finance", SessionID: "ines-as-finance"})
	require.NoError(t, err)
	// tam, a member of legal, casts onto legal.
	_, err = core.OpenTeamCast(ctx, authority.Person{ID: "tam", Organization: "acme", SessionID: "tam-session"},
		authority.TeamCastRequest{Organization: "acme", Team: "legal", SessionID: "tam-as-legal"})
	require.NoError(t, err)

	t.Run("no session: the person as themselves, on both halves", func(t *testing.T) {
		v, err := s.entitlementViewerOf(ctx, "acme", "tam", "")
		require.NoError(t, err)
		require.Equal(t, authority.Person{ID: "tam", Organization: "acme"}, v.Person)
		require.Equal(t, "tam", v.Subject)
		require.Equal(t, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, v.SubjectKind)
		require.ElementsMatch(t, []string{"inst-own", "inst-legal"}, held(v.Person), "their own grant and their team's")
	})

	t.Run("a session with no view: the person as themselves, asking from it", func(t *testing.T) {
		v, err := s.entitlementViewerOf(ctx, "acme", "tam", "tam-session")
		require.NoError(t, err)
		require.Equal(t, "tam-session", v.Person.SessionID)
		require.Equal(t, "tam", v.Subject)
		require.Equal(t, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, v.SubjectKind)
		require.ElementsMatch(t, []string{"inst-own", "inst-legal"}, held(v.Person))
	})

	t.Run("a cast: the team's pages, and none of the person's own", func(t *testing.T) {
		v, err := s.entitlementViewerOf(ctx, "acme", "tam", "tam-as-legal")
		require.NoError(t, err)
		require.Equal(t, authority.Person{ID: "tam", Organization: "acme", SessionID: "tam-as-legal"}, v.Person,
			"the core half reads the person under the session's view")
		require.Equal(t, "legal", v.Subject, "this host's grant half reads the team's grants")
		require.Equal(t, gen.SubjectKind_SUBJECT_KIND_TEAM, v.SubjectKind)
		require.Equal(t, []string{"inst-legal"}, held(v.Person), "the team's page, not the page their direct grant reaches")
	})

	t.Run("another person's cast is refused, never answered as the subject", func(t *testing.T) {
		_, err := s.entitlementViewerOf(ctx, "acme", "tam", "ines-as-finance")
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		_, err = core.HeldInstallations(ctx, authority.Person{ID: "tam", Organization: "acme", SessionID: "ines-as-finance"}, "acme")
		require.Error(t, err, "the core refuses it too")
	})
}
