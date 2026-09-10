package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// principalLookupStore is the minimum store surface actorTypeForCreator
// touches: a scope to run the lookup in, and the principal it resolves.
type principalLookupStore struct {
	Store
	principal *Principal
}

func (s *principalLookupStore) As(Identity) Scoped { return &principalLookupScope{} }

func (s *principalLookupStore) GetPrincipal(context.Context, string) (*Principal, error) {
	return s.principal, nil
}

func (s *principalLookupStore) GetAgentPrincipal(context.Context, string, string) (*Principal, error) {
	panic("not used by actorTypeForCreator")
}
func (s *principalLookupStore) CreateAgentPrincipal(context.Context, *Principal) error {
	panic("not used by actorTypeForCreator")
}
func (s *principalLookupStore) RevokePrincipal(context.Context, string, string) error {
	panic("not used by actorTypeForCreator")
}
func (s *principalLookupStore) DisableAgentPrincipal(context.Context, string, string) (bool, error) {
	panic("not used by actorTypeForCreator")
}
func (s *principalLookupStore) EnableAgentPrincipal(context.Context, string) (bool, error) {
	panic("not used by actorTypeForCreator")
}
func (s *principalLookupStore) ListPrincipals(context.Context, string, string, int32, string) ([]*Principal, string, error) {
	panic("not used by actorTypeForCreator")
}

type principalLookupScope struct{ Scoped }

func (s *principalLookupScope) Within(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// TestActorTypeForCreator_SpeaksTheRegisteredVocabulary is the regression for
// the drift the actor-type gate exists to catch. actorTypeForCreator returned
// the literal "service" for a service principal — a value
// audit_events.actor_type does not admit. The row was rejected by the CHECK
// constraint, and because the emit runs on the caller's transaction that
// rejection failed CreateAgentPrincipal itself rather than only its record.
//
// It is also the runtime guarantee auditActorTypeIndirectSites names for
// principals.go, which is why every branch is exercised here.
func TestActorTypeForCreator_SpeaksTheRegisteredVocabulary(t *testing.T) {
	registered := map[string]bool{
		ActorTypeUser:   true,
		ActorTypeAPIKey: true,
		ActorTypeSystem: true,
		ActorTypeAgent:  true,
	}

	for name, tc := range map[string]struct {
		kind string
		want string
	}{
		"an agent principal is agent work":   {PrincipalKindAgent, ActorTypeAgent},
		"a service principal is system work": {PrincipalKindService, ActorTypeSystem},
		"a human principal is user work":     {PrincipalKindHuman, ActorTypeUser},
		"an unknown kind falls back to user": {"something-new", ActorTypeUser},
	} {
		t.Run(name, func(t *testing.T) {
			svc, err := NewService(&principalLookupStore{
				principal: &Principal{ID: "019f6bf7-6a01-7001-8001-0000000000d1", Kind: tc.kind},
			})
			require.NoError(t, err)

			got := svc.actorTypeForCreator(context.Background(), "019f6bf7-6a01-7001-8001-0000000000d1")
			require.Equal(t, tc.want, got)
			require.True(t, registered[got],
				"actor_type %q is not one the audit_events CHECK constraint admits, so the emit would abort the mutation", got)
		})
	}

	t.Run("no creator is user work and consults no store", func(t *testing.T) {
		svc, err := NewService(&principalLookupStore{})
		require.NoError(t, err)
		require.Equal(t, ActorTypeUser, svc.actorTypeForCreator(context.Background(), ""))
	})
}
