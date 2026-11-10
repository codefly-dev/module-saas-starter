package adapters

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// identityLinkStore records the identities linked to users; platformRoles are
// the roles the platform assigned.
type identityLinkStore struct {
	business.Store
	platformRoles map[string]string
	linked        []*gen.UserIdentity
}

type identityLinkScope struct{ business.Scoped }

func (identityLinkScope) Within(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *identityLinkStore) As(business.Identity) business.Scoped { return identityLinkScope{} }

func (s *identityLinkStore) GetPlatformRole(_ context.Context, userID string) (string, error) {
	return s.platformRoles[userID], nil
}

func (s *identityLinkStore) AddIdentity(_ context.Context, identity *gen.UserIdentity) error {
	s.linked = append(s.linked, identity)
	return nil
}

// A provider identity chosen by the caller, linked to a user, is a way in as
// that user: sign-in resolves the user from exactly that (provider, subject)
// row. Linking one to another person's account is therefore taking it over,
// and no platform role — support and billing included — may do it. Before this
// test's change any non-empty platform role passed the gate.
func TestNoPlatformRoleLinksAnIdentityToAnotherUsersAccount(t *testing.T) {
	victim, operator := uuid.NewString(), uuid.NewString()
	for _, role := range []string{"support", "billing", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			store := &identityLinkStore{platformRoles: map[string]string{operator: role}}
			previous := service
			t.Cleanup(func() { service = previous })
			svc, err := business.NewService(store)
			require.NoError(t, err)
			WithService(svc)

			link := func(actor string) error {
				ctx := accountsauth.WithVerifiedRequestIdentity(context.Background(), accountsauth.RequestIdentity{RealActor: uuid.MustParse(actor), EffectiveSubject: uuid.MustParse(actor)})
				_, err := (&UserServer{}).AddIdentity(ctx, &gen.AddIdentityRequest{
					UserUuid: victim,
					Identity: &gen.UserIdentity{Provider: "workos", ProviderId: "attacker-subject", ProviderEmail: "attacker@example.test", EmailVerified: true},
				})
				return err
			}

			err = link(operator)
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%s linked an identity to another user's account: %v", role, err)
			require.Contains(t, err.Error(), "own account", "the refusal says what is allowed")
			require.Empty(t, store.linked, "nothing was written")

			require.NoError(t, link(victim), "a user links an identity to their own account")
			require.Len(t, store.linked, 1)
		})
	}
}
