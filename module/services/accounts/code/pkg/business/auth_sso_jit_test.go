package business

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

type fakeSsoRouter struct {
	orgID      uuid.UUID
	hasPolicy  bool
	err        error
	calledWith string
	callCount  int
}

func (f *fakeSsoRouter) ResolveOrgProvisioning(_ context.Context, providerOrgID string) (uuid.UUID, bool, error) {
	f.callCount++
	f.calledWith = providerOrgID
	return f.orgID, f.hasPolicy, f.err
}

func TestSelectSsoJitIntent(t *testing.T) {
	orgID := uuid.New()
	base := auth.SignupIntent{OrganizationName: "ignored"}

	t.Run("global provider keeps the base intent and never consults the router", func(t *testing.T) {
		router := &fakeSsoRouter{orgID: orgID, hasPolicy: true}
		got := selectSsoJitIntent(context.Background(), base, &auth.Claims{ProviderOrgID: ""}, router)
		require.Equal(t, base, got)
		require.Zero(t, router.callCount, "a global-provider login must not touch the SSO router")
	})

	t.Run("nil router keeps the base intent", func(t *testing.T) {
		got := selectSsoJitIntent(context.Background(), base, &auth.Claims{ProviderOrgID: "workos-1"}, nil)
		require.Equal(t, base, got)
	})

	t.Run("invitation token wins over SSO JIT", func(t *testing.T) {
		router := &fakeSsoRouter{orgID: orgID, hasPolicy: true}
		invite := auth.InviteIntent{Token: "tok"}
		got := selectSsoJitIntent(context.Background(), invite, &auth.Claims{ProviderOrgID: "workos-1"}, router)
		require.Equal(t, invite, got)
	})

	t.Run("org with a policy upgrades to SsoJitIntent", func(t *testing.T) {
		router := &fakeSsoRouter{orgID: orgID, hasPolicy: true}
		got := selectSsoJitIntent(context.Background(), base, &auth.Claims{ProviderOrgID: "workos-1"}, router)
		require.Equal(t, auth.SsoJitIntent{OrgID: orgID}, got)
		require.Equal(t, "workos-1", router.calledWith)
	})

	t.Run("org without a policy keeps the base intent", func(t *testing.T) {
		router := &fakeSsoRouter{orgID: orgID, hasPolicy: false}
		got := selectSsoJitIntent(context.Background(), base, &auth.Claims{ProviderOrgID: "workos-1"}, router)
		require.Equal(t, base, got)
	})

	t.Run("router error falls back to the base intent", func(t *testing.T) {
		router := &fakeSsoRouter{err: errors.New("db down")}
		got := selectSsoJitIntent(context.Background(), base, &auth.Claims{ProviderOrgID: "workos-1"}, router)
		require.Equal(t, base, got)
	})
}
