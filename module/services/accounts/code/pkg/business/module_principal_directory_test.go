package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// pagedPrincipalStore answers ListPrincipals with a fixed page, filtered by kind
// the way the real store's kind predicate filters, so a test can say whether
// the stored rows continue past it.
type pagedPrincipalStore struct {
	principalLookupStore
	rows []*Principal
	next string

	// gotPageSize records the budget the business layer actually asked the store
	// for, which is how a test can say whether the projected modules were taken
	// out of the caller's page or added on top of it.
	gotPageSize int32
}

func (s *pagedPrincipalStore) ListPrincipals(_ context.Context, _, kind string, pageSize int32, _ string) ([]*Principal, string, error) {
	s.gotPageSize = pageSize
	var out []*Principal
	for _, p := range s.rows {
		if kind == "" || p.Kind == kind {
			out = append(out, p)
		}
	}
	return out, s.next, nil
}

// A composed module is a principal with no stored row: it acts in the org, holds
// grants there, and is the actor of the audit rows it records. The directory an
// org's principals are named from lists it — by its prefix, as a service
// principal — or every surface built on that directory shows it as unknown
// ("Actor unavailable" on the audit log). Only modules that may act in the org
// are listed: the one bound to it and the cross-tenant ones, never another
// org's.
//
// They lead the FIRST page. A directory walk reads a bounded number of pages
// and stops once every actor it wants is named, so a module listed only on the
// last page is never reached in an org with more principals than the walk
// reads — the org where naming it matters most.
//
// The registry is parsed from the deployment document rather than built by
// hand, so it has the shape production runs with: every grant declares a
// tenant, the cross-tenant module's being another org. The documents module's
// tenant is declared in uppercase; it is still this org.
func TestListPrincipals_NamesTheModulesActingInTheOrg(t *testing.T) {
	const (
		org      = "019f6bf7-6a01-7001-8001-0000000000c1"
		orgUpper = "019F6BF7-6A01-7001-8001-0000000000C1"
		otherOrg = "019f6bf7-6a01-7001-8001-0000000000c2"
	)
	human := &Principal{ID: "019f6bf7-6a01-7001-8001-0000000000a1", Kind: PrincipalKindHuman, DisplayName: "admin@example.com"}
	registry, err := ParseModulePrincipalRegistry(`{
		"documents": {"tenant": "` + orgUpper + `"},
		"assistant": {"tenant": "` + otherOrg + `", "cross_tenant": true},
		"elsewhere": {"tenant": "` + otherOrg + `"}
	}`)
	require.NoError(t, err)
	list := func(t *testing.T, orgID, kind, pageToken, next string) ([]*Principal, string) {
		t.Helper()
		svc, err := NewService(&pagedPrincipalStore{rows: []*Principal{human}, next: next})
		require.NoError(t, err)
		withCurrentAuthority(svc)
		withCurrentAuthority(svc)
		svc.SetModulePrincipals(registry)
		out, token, err := svc.ListPrincipals(context.Background(), orgID, kind, 50, pageToken)
		require.NoError(t, err)
		return out, token
	}
	assistant := &Principal{ID: ModulePrincipalID("assistant"), Kind: PrincipalKindService, DisplayName: "assistant", OrgID: org}
	documents := &Principal{ID: ModulePrincipalID("documents"), Kind: PrincipalKindService, DisplayName: "documents", OrgID: org}

	out, token := list(t, org, "", "", "")
	require.Empty(t, token)
	require.Equal(t, []*Principal{assistant, documents, human}, out,
		"the org's modules lead, ordered by prefix; the stored rows follow untouched")

	firstPage, token := list(t, org, "", "", "more")
	require.Equal(t, "more", token, "the stored rows' cursor is passed through unchanged")
	require.Equal(t, []*Principal{assistant, documents, human}, firstPage,
		"the modules are on the first page even when more stored rows follow")

	laterPage, _ := list(t, org, "", "cursor", "")
	require.Equal(t, []*Principal{human}, laterPage, "a later page lists no module a second time")

	serviceOnly, _ := list(t, org, PrincipalKindService, "", "")
	require.Equal(t, []*Principal{assistant, documents}, serviceOnly, "a module principal is a service principal")

	humans, _ := list(t, org, PrincipalKindHuman, "", "")
	require.Equal(t, []*Principal{human}, humans, "a kind filter that excludes services lists no module")

	upper, _ := list(t, orgUpper, "", "", "")
	require.Equal(t, []*Principal{assistant, documents, human}, upper,
		"an org id spelled in uppercase still names its own modules")
}

// A deployment that spells a module's tenant in any form uuid.Parse accepts gets
// the canonical form back. Every downstream comparison — the directory, the
// operation-context revision check, the bound org a Work Context seals — is a
// string compare against the lowercase id Postgres returns, so a non-canonical
// spelling would pass validation and then match its own organization nowhere.
func TestParseModulePrincipalRegistry_CanonicalizesTheTenant(t *testing.T) {
	const canonical = "019f6bf7-6a01-7001-8001-0000000000c1"
	for _, spelling := range []string{
		canonical,
		"019F6BF7-6A01-7001-8001-0000000000C1",
		"{019f6bf7-6a01-7001-8001-0000000000c1}",
		"urn:uuid:019f6bf7-6a01-7001-8001-0000000000c1",
		"019f6bf76a01700180010000000000c1",
	} {
		t.Run(spelling, func(t *testing.T) {
			registry, err := ParseModulePrincipalRegistry(`{"documents": {"tenant": "` + spelling + `"}}`)
			require.NoError(t, err)
			require.Equal(t, canonical, registry[ModulePrincipalID("documents")].Tenant)
		})
	}
}

// The projected modules come out of the page's own budget, so a page still holds
// at most the pageSize rows the caller asked for, and the keyset token still
// describes exactly the stored rows the store returned. Adding them on top would
// hand a caller more rows than it asked for and a token that does not account for
// them.
func TestListPrincipals_ModulesComeOutOfThePageBudget(t *testing.T) {
	const (
		org      = "019f6bf7-6a01-7001-8001-0000000000c1"
		otherOrg = "019f6bf7-6a01-7001-8001-0000000000c2"
	)
	human := &Principal{ID: "019f6bf7-6a01-7001-8001-0000000000a1", Kind: PrincipalKindHuman, DisplayName: "admin@example.com"}
	registry, err := ParseModulePrincipalRegistry(`{
		"documents": {"tenant": "` + org + `"},
		"assistant": {"tenant": "` + otherOrg + `", "cross_tenant": true}
	}`)
	require.NoError(t, err)
	list := func(t *testing.T, pageToken, next string) ([]*Principal, *pagedPrincipalStore) {
		t.Helper()
		store := &pagedPrincipalStore{rows: []*Principal{human}, next: next}
		svc, err := NewService(store)
		require.NoError(t, err)
		withCurrentAuthority(svc)
		withCurrentAuthority(svc)
		svc.SetModulePrincipals(registry)
		out, _, err := svc.ListPrincipals(context.Background(), org, "", 50, pageToken)
		require.NoError(t, err)
		return out, store
	}

	first, firstStore := list(t, "", "more")
	require.Len(t, first, 3, "the two modules acting here, and the stored row")
	require.Equal(t, int32(48), firstStore.gotPageSize,
		"the two modules take two of the fifty rows the caller asked for")

	// A page reached through a token carries no module, so the whole budget is the
	// store's.
	later, laterStore := list(t, "cursor", "")
	require.Equal(t, []*Principal{human}, later)
	require.Equal(t, int32(50), laterStore.gotPageSize)
}
