package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// pagedPrincipalStore answers ListPrincipals with a fixed page, so a test can
// say whether the stored rows continue past it.
type pagedPrincipalStore struct {
	principalLookupStore
	rows []*Principal
	next string
}

func (s *pagedPrincipalStore) ListPrincipals(context.Context, string, string, int32, string) ([]*Principal, string, error) {
	return s.rows, s.next, nil
}

// A composed module is a principal with no stored row: it acts in the org, holds
// grants there, and is the actor of the audit rows it records. The directory an
// org's principals are named from lists it — by its prefix, as a service
// principal — or every surface built on that directory shows it as unknown
// ("Actor unavailable" on the audit log). Only modules that may act in the org
// are listed: the one bound to it and the cross-tenant ones, never another
// org's.
func TestListPrincipals_NamesTheModulesActingInTheOrg(t *testing.T) {
	const (
		org      = "019f6bf7-6a01-7001-8001-0000000000c1"
		otherOrg = "019f6bf7-6a01-7001-8001-0000000000c2"
	)
	human := &Principal{ID: "019f6bf7-6a01-7001-8001-0000000000a1", Kind: PrincipalKindHuman, DisplayName: "admin@example.com"}
	registry := ModulePrincipalRegistry{
		ModulePrincipalID("documents"): {Prefix: "documents", Tenant: org},
		ModulePrincipalID("assistant"): {Prefix: "assistant", CrossTenant: true},
		ModulePrincipalID("elsewhere"): {Prefix: "elsewhere", Tenant: otherOrg},
	}
	list := func(t *testing.T, kind, next string) ([]*Principal, string) {
		t.Helper()
		svc, err := NewService(&pagedPrincipalStore{rows: []*Principal{human}, next: next})
		require.NoError(t, err)
		svc.SetModulePrincipals(registry)
		out, token, err := svc.ListPrincipals(context.Background(), org, kind, 50, "")
		require.NoError(t, err)
		return out, token
	}

	out, token := list(t, "", "")
	require.Empty(t, token)
	require.Len(t, out, 3)
	require.Equal(t, human, out[0], "stored principals come first, untouched")
	require.Equal(t, &Principal{ID: ModulePrincipalID("assistant"), Kind: PrincipalKindService, DisplayName: "assistant", OrgID: org}, out[1])
	require.Equal(t, &Principal{ID: ModulePrincipalID("documents"), Kind: PrincipalKindService, DisplayName: "documents", OrgID: org}, out[2])

	serviceOnly, _ := list(t, PrincipalKindService, "")
	require.Len(t, serviceOnly, 3, "a module principal is a service principal")

	humans, _ := list(t, PrincipalKindHuman, "")
	require.Equal(t, []*Principal{human}, humans, "a kind filter that excludes services lists no module")

	firstPage, token := list(t, "", "more")
	require.Equal(t, "more", token)
	require.Equal(t, []*Principal{human}, firstPage, "modules follow the stored rows, on the last page only")
}
