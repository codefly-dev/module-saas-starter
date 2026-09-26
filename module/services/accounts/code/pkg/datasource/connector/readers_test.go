package connector_test

import (
	"context"
	"reflect"
	"testing"

	"accounts/pkg/datasource/connector"
	accountsv1 "accounts/pkg/gen/saas/accounts/v1"
)

type fakeDirectory struct {
	links    map[string]string
	bindings map[string]string
	verified map[string]bool
}

func (d fakeDirectory) LinkedUsers(_ context.Context, _ string, ids []string) (map[string]string, error) {
	return pick(d.links, ids), nil
}

func (d fakeDirectory) BoundTeams(_ context.Context, _ string, ids []string) (map[string]string, error) {
	return pick(d.bindings, ids), nil
}

func (d fakeDirectory) VerifiedDomains(_ context.Context, _ string, domains []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, dom := range domains {
		out[dom] = d.verified[dom]
	}
	return out, nil
}

func pick(m map[string]string, ids []string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if v, ok := m[id]; ok {
			out[id] = v
		}
	}
	return out
}

var dir = fakeDirectory{
	links:    map[string]string{"acct-jane": "user-jane"},
	bindings: map[string]string{"grp-eng": "team-eng"},
	verified: map[string]bool{"example.com": true},
}

const (
	basisTranslated     = accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_TRANSLATED
	basisUntranslatable = accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_UNTRANSLATABLE
)

func TestTranslateAppliesTheSettledRules(t *testing.T) {
	cases := []struct {
		name  string
		acl   []connector.ProviderPrincipal
		basis accountsv1.DatasourceItemReadersBasis
		users []string
		teams []string
		bound bool
		lost  int32
	}{
		{"a linked account is its host user", []connector.ProviderPrincipal{{Kind: connector.PrincipalAccount, ID: "acct-jane"}},
			basisTranslated, []string{"user-jane"}, nil, false, 0},
		{"an unlinked account is dropped, never matched by email", []connector.ProviderPrincipal{{Kind: connector.PrincipalAccount, ID: "user@example.com"}, {Kind: connector.PrincipalAccount, ID: "acct-jane"}},
			basisTranslated, []string{"user-jane"}, nil, false, 1},
		{"a bound group is its host team", []connector.ProviderPrincipal{{Kind: connector.PrincipalGroup, ID: "grp-eng"}},
			basisTranslated, nil, []string{"team-eng"}, false, 0},
		{"an unbound group grants nothing", []connector.ProviderPrincipal{{Kind: connector.PrincipalGroup, ID: "grp-sales"}},
			basisUntranslatable, nil, nil, false, 1},
		{"a guest is dropped and counted", []connector.ProviderPrincipal{{Kind: connector.PrincipalGuest, ID: "guest-1"}, {Kind: connector.PrincipalAccount, ID: "acct-jane"}},
			basisTranslated, []string{"user-jane"}, nil, false, 1},
		{"anyone with the link is admin-only", []connector.ProviderPrincipal{{Kind: connector.PrincipalAccount, ID: "acct-jane"}, {Kind: connector.PrincipalAnyoneWithLink}},
			basisUntranslatable, nil, nil, false, 1},
		{"a verified domain is the boundary's readers", []connector.ProviderPrincipal{{Kind: connector.PrincipalDomain, ID: "example.com"}},
			basisTranslated, nil, nil, true, 0},
		{"an unverified domain is dropped", []connector.ProviderPrincipal{{Kind: connector.PrincipalDomain, ID: "example.org"}},
			basisUntranslatable, nil, nil, false, 1},
		{"an empty access list is admin-only", nil, basisUntranslatable, nil, nil, false, 0},
		{"an unknown kind is dropped", []connector.ProviderPrincipal{{Kind: "everyone", ID: "*"}},
			basisUntranslatable, nil, nil, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := connector.Translate(context.Background(), dir, "drive", "org-1", connector.ProviderACL{Entries: tc.acl, Version: "v7"})
			if err != nil {
				t.Fatal(err)
			}
			if r.GetBasis() != tc.basis || !reflect.DeepEqual(nonNil(r.GetUserIds()), nonNil(tc.users)) ||
				!reflect.DeepEqual(nonNil(r.GetGroupIds()), nonNil(tc.teams)) || r.GetBoundaryReaders() != tc.bound ||
				r.GetUnmappedCount() != tc.lost || r.GetAclVersion() != "v7" {
				t.Fatalf("readers = %v", r)
			}
			if msg := connector.ReadersError(r); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestPersonalSourceIsItsOwnersAlone(t *testing.T) {
	src := connector.Source{ID: "s", PersonalOwnerUserID: "user-jane"}
	for _, in := range []*accountsv1.DatasourceItemReaders{connector.SourceScoped(), connector.Untranslatable(0, ""),
		{Basis: basisTranslated, GroupIds: []string{"team-eng"}, BoundaryReaders: true}} {
		r := connector.ApplySourcePolicy(src, in)
		if !reflect.DeepEqual(r.GetUserIds(), []string{"user-jane"}) || r.GetBoundaryReaders() || len(r.GetGroupIds()) > 0 {
			t.Fatalf("personal readers = %v", r)
		}
	}
	shared := connector.Source{ID: "s"}
	if r := connector.ApplySourcePolicy(shared, connector.SourceScoped()); !r.GetBoundaryReaders() {
		t.Fatal("a shared source keeps its connector's readers")
	}
}

func TestReadersErrorRefusesWideningShapes(t *testing.T) {
	bad := []*accountsv1.DatasourceItemReaders{
		nil,
		{},
		{Basis: accountsv1.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_SOURCE_SCOPED, BoundaryReaders: true, UserIds: []string{"u"}},
		{Basis: basisUntranslatable, BoundaryReaders: true},
		{Basis: basisTranslated},
	}
	for _, r := range bad {
		if connector.ReadersError(r) == "" {
			t.Fatalf("readers %v must be refused", r)
		}
	}
}
