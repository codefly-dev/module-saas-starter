package business_test

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/github"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/githubconnector"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// directoryStore is the datasource directory's rows in memory, over the
// datasource fake, with the organization's teams and memberships.
type directoryStore struct {
	*datasourceFakeStore
	dmu      sync.Mutex
	links    map[string]*business.DatasourceAccountLink
	bindings map[string]*business.DatasourceGroupBinding
	domains  map[string]*business.DatasourceDomain
	teams    map[string]string // team id -> org id
	roles    map[string]gen.OrgRole
	budgets  map[string]*budgetRow
}

type budgetRow struct {
	started time.Time
	used    int
	blocked time.Time
}

func newDirectoryStore() *directoryStore {
	return &directoryStore{
		datasourceFakeStore: newDatasourceFakeStore(),
		links:               map[string]*business.DatasourceAccountLink{},
		bindings:            map[string]*business.DatasourceGroupBinding{},
		domains:             map[string]*business.DatasourceDomain{},
		teams:               map[string]string{},
		roles:               map[string]gen.OrgRole{},
		budgets:             map[string]*budgetRow{},
	}
}

func (d *directoryStore) UpsertDatasourceAccountLink(_ context.Context, link *business.DatasourceAccountLink) (*business.DatasourceAccountLink, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	for _, l := range d.links {
		if l.OrgID == link.OrgID && l.Connector == link.Connector && l.ProviderAccountID == link.ProviderAccountID {
			if l.UserID != link.UserID {
				return nil, business.ErrDatasourceAccountLinkedElsewhere
			}
			l.ProviderAccountLogin = link.ProviderAccountLogin
			cp := *l
			return &cp, nil
		}
	}
	cp := *link
	cp.CreatedAt = time.Now().UTC()
	d.links[cp.ID] = &cp
	out := cp
	return &out, nil
}

func (d *directoryStore) ListDatasourceAccountLinks(_ context.Context, orgID, userID string) ([]*business.DatasourceAccountLink, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	var out []*business.DatasourceAccountLink
	for _, l := range d.links {
		if l.OrgID == orgID && (userID == "" || l.UserID == userID) {
			cp := *l
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (d *directoryStore) GetDatasourceAccountLink(_ context.Context, orgID, id string) (*business.DatasourceAccountLink, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	if l, ok := d.links[id]; ok && l.OrgID == orgID {
		cp := *l
		return &cp, nil
	}
	return nil, nil
}

func (d *directoryStore) DeleteDatasourceAccountLink(_ context.Context, orgID, id string) error {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	if l, ok := d.links[id]; ok && l.OrgID == orgID {
		delete(d.links, id)
	}
	return nil
}

func (d *directoryStore) InsertDatasourceGroupBinding(_ context.Context, b *business.DatasourceGroupBinding) error {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	for _, x := range d.bindings {
		if x.OrgID == b.OrgID && x.Connector == b.Connector && x.ProviderGroupID == b.ProviderGroupID {
			return business.ErrDatasourceGroupAlreadyBound
		}
	}
	cp := *b
	d.bindings[b.ID] = &cp
	return nil
}

func (d *directoryStore) ListDatasourceGroupBindings(_ context.Context, orgID string) ([]*business.DatasourceGroupBinding, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	var out []*business.DatasourceGroupBinding
	for _, b := range d.bindings {
		if b.OrgID == orgID {
			cp := *b
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (d *directoryStore) DeleteDatasourceGroupBinding(_ context.Context, orgID, id string) (*business.DatasourceGroupBinding, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	b, ok := d.bindings[id]
	if !ok || b.OrgID != orgID {
		return nil, nil
	}
	delete(d.bindings, id)
	return b, nil
}

func (d *directoryStore) InsertDatasourceDomain(_ context.Context, dom *business.DatasourceDomain) error {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	for _, x := range d.domains {
		if x.OrgID == dom.OrgID && x.Domain == dom.Domain {
			return business.ErrDatasourceDomainAlreadyClaimed
		}
	}
	cp := *dom
	d.domains[dom.ID] = &cp
	return nil
}

func (d *directoryStore) ListDatasourceDomains(_ context.Context, orgID string) ([]*business.DatasourceDomain, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	var out []*business.DatasourceDomain
	for _, x := range d.domains {
		if x.OrgID == orgID {
			cp := *x
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (d *directoryStore) GetDatasourceDomain(_ context.Context, orgID, id string) (*business.DatasourceDomain, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	if x, ok := d.domains[id]; ok && x.OrgID == orgID {
		cp := *x
		return &cp, nil
	}
	return nil, nil
}

func (d *directoryStore) MarkDatasourceDomainVerified(_ context.Context, orgID, id string, at time.Time) error {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	if x, ok := d.domains[id]; ok && x.OrgID == orgID && x.VerifiedAt == nil {
		x.VerifiedAt = &at
	}
	return nil
}

func (d *directoryStore) DeleteDatasourceDomain(_ context.Context, orgID, id string) (*business.DatasourceDomain, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	x, ok := d.domains[id]
	if !ok || x.OrgID != orgID {
		return nil, nil
	}
	delete(d.domains, id)
	return x, nil
}

func (d *directoryStore) LinkedDatasourceUsers(_ context.Context, orgID, connectorKey string, ids []string) (map[string]string, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	out := map[string]string{}
	for _, l := range d.links {
		for _, id := range ids {
			if l.OrgID == orgID && l.Connector == connectorKey && l.ProviderAccountID == id {
				out[id] = l.UserID
			}
		}
	}
	return out, nil
}

func (d *directoryStore) BoundDatasourceTeams(_ context.Context, orgID, connectorKey string, ids []string) (map[string]string, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	out := map[string]string{}
	for _, b := range d.bindings {
		for _, id := range ids {
			if b.OrgID == orgID && b.Connector == connectorKey && b.ProviderGroupID == id {
				out[id] = b.TeamID
			}
		}
	}
	return out, nil
}

func (d *directoryStore) VerifiedDatasourceDomains(_ context.Context, orgID string, domains []string) (map[string]bool, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	out := map[string]bool{}
	for _, x := range d.domains {
		for _, dom := range domains {
			if x.OrgID == orgID && x.Domain == dom && x.VerifiedAt != nil {
				out[dom] = true
			}
		}
	}
	return out, nil
}

func (d *directoryStore) GetTeamOrgID(_ context.Context, teamID string) (string, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	org, ok := d.teams[teamID]
	if !ok {
		return "", errors.New("team not found")
	}
	return org, nil
}

func (d *directoryStore) ListTeams(_ context.Context, orgID, _ string) ([]*gen.Team, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	var out []*gen.Team
	for id, org := range d.teams {
		if org == orgID {
			out = append(out, &gen.Team{Id: id, OrgId: org, Name: "team " + id[:4]})
		}
	}
	return out, nil
}

func (d *directoryStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	role, ok := d.roles[userID]
	if !ok {
		return nil, nil
	}
	return &gen.OrgMembership{OrgId: orgID, UserId: userID, Role: role}, nil
}

func (d *directoryStore) SpendDatasourceBudget(_ context.Context, key string, limit int, window time.Duration, now time.Time) (bool, time.Time, time.Time, error) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	row, ok := d.budgets[key]
	if !ok {
		row = &budgetRow{started: now}
		d.budgets[key] = row
	}
	if now.Before(row.blocked) {
		return false, row.started.Add(window), row.blocked, nil
	}
	if !now.Before(row.started.Add(window)) {
		row.started, row.used = now, 0
	}
	if row.used >= limit {
		return false, row.started.Add(window), time.Time{}, nil
	}
	row.used++
	return true, row.started.Add(window), time.Time{}, nil
}

func (d *directoryStore) BlockDatasourceBudget(_ context.Context, key string, until time.Time) error {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	row, ok := d.budgets[key]
	if !ok {
		row = &budgetRow{started: time.Now()}
		d.budgets[key] = row
	}
	if until.After(row.blocked) {
		row.blocked = until
	}
	return nil
}

// fakeLinker proves accounts by a code table: code -> (account id, login).
type fakeLinker struct{ accounts map[string][2]string }

func (f fakeLinker) AuthorizeURL(state, redirectURI string) string {
	return "https://provider.example.com/authorize?state=" + state + "&redirect_uri=" + redirectURI
}

func (f fakeLinker) ResolveAccount(_ context.Context, code string) (string, string, error) {
	a, ok := f.accounts[code]
	if !ok {
		return "", "", githubconnector.ErrUserCodeRejected
	}
	return a[0], a[1], nil
}

type fakeTXT map[string][]string

func (f fakeTXT) LookupTXT(_ context.Context, name string) ([]string, error) {
	if r, ok := f[name]; ok {
		return r, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

const (
	userJane = "33333333-3333-3333-3333-333333333333"
	userJoe  = "44444444-4444-4444-4444-444444444444"
	userAdm  = "55555555-5555-5555-5555-555555555555"
	teamEng  = "66666666-6666-6666-6666-666666666666"
	teamElse = "77777777-7777-7777-7777-777777777777"
)

func newDirectoryService(t *testing.T) (*business.Service, *directoryStore, *recordingAudit) {
	t.Helper()
	store := newDirectoryStore()
	store.teams[teamEng] = testOrg
	store.teams[teamElse] = "99999999-9999-9999-9999-999999999999"
	store.roles[userJane] = gen.OrgRole_ORG_ROLE_MEMBER
	store.roles[userJoe] = gen.OrgRole_ORG_ROLE_MEMBER
	store.roles[userAdm] = gen.OrgRole_ORG_ROLE_ADMIN
	svc, audit := newDatasourceService(store, &recordingProducer{}, nil)
	svc.SetDatasourceKeys([]byte("test-ticket-key"), []byte("test-link-key"))
	svc.SetDatasourceAccountLinkers(map[string]business.DatasourceAccountLinker{
		"github": fakeLinker{accounts: map[string][2]string{"code-jane": {"1001", "jane"}, "code-joe": {"1002", "joe"}}},
	})
	return svc, store, audit
}

func link(t *testing.T, svc *business.Service, user, code string) (*business.DatasourceAccountLink, error) {
	t.Helper()
	handle, err := svc.BeginDatasourceAccountLink(context.Background(), user, testOrg, "github", "https://host.example.com/return")
	require.NoError(t, err)
	require.Contains(t, handle.AuthorizeURL, "redirect_uri=https://host.example.com/return")
	return svc.CompleteDatasourceAccountLink(context.Background(), user, testOrg, handle.State, code)
}

func TestAccountLinkProvesTheAccountBySigningIn(t *testing.T) {
	svc, _, audit := newDirectoryService(t)
	l, err := link(t, svc, userJane, "code-jane")
	require.NoError(t, err)
	require.Equal(t, "1001", l.ProviderAccountID)
	require.Equal(t, userJane, l.UserID)
	require.True(t, auditHas(audit, business.EventDatasourceAccountLinked))
	requireDeclaredPayloads(t, audit)

	mine, err := svc.ListMyDatasourceAccountLinks(context.Background(), userJane, testOrg)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	theirs, err := svc.ListMyDatasourceAccountLinks(context.Background(), userJoe, testOrg)
	require.NoError(t, err)
	require.Empty(t, theirs)
}

func TestAccountLinkStateIsBoundToWhoBeganIt(t *testing.T) {
	svc, _, _ := newDirectoryService(t)
	handle, err := svc.BeginDatasourceAccountLink(context.Background(), userJane, testOrg, "github", "")
	require.NoError(t, err)
	// Another person redeeming Jane's state, the right person in another org, a
	// tampered state and a code the provider rejects are all refused.
	_, err = svc.CompleteDatasourceAccountLink(context.Background(), userJoe, testOrg, handle.State, "code-joe")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.CompleteDatasourceAccountLink(context.Background(), userJane, "88888888-8888-8888-8888-888888888888", handle.State, "code-jane")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.CompleteDatasourceAccountLink(context.Background(), userJane, testOrg, handle.State+"x", "code-jane")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = svc.CompleteDatasourceAccountLink(context.Background(), userJane, testOrg, handle.State, "forged")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestAnAccountLinksToOnePersonOnly(t *testing.T) {
	svc, _, _ := newDirectoryService(t)
	_, err := link(t, svc, userJane, "code-jane")
	require.NoError(t, err)
	// Joe signing in as Jane's account (a shared password, say) cannot take it.
	_, err = link(t, svc, userJoe, "code-jane")
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	// Jane linking again is the same link.
	again, err := link(t, svc, userJane, "code-jane")
	require.NoError(t, err)
	require.Equal(t, userJane, again.UserID)
}

func TestAccountLinkRemoval(t *testing.T) {
	svc, store, _ := newDirectoryService(t)
	l, err := link(t, svc, userJane, "code-jane")
	require.NoError(t, err)
	err = svc.DeleteDatasourceAccountLink(context.Background(), userJoe, testOrg, l.ID)
	require.Equal(t, codes.NotFound, status.Code(err), "a member may not remove someone else's link")
	require.NoError(t, svc.DeleteDatasourceAccountLink(context.Background(), userAdm, testOrg, l.ID), "an administrator may")
	require.Empty(t, store.links)
	l, err = link(t, svc, userJane, "code-jane")
	require.NoError(t, err)
	require.NoError(t, svc.DeleteDatasourceAccountLink(context.Background(), userJane, testOrg, l.ID), "and so may its owner")
}

func TestAccountLinkNeedsAProviderSignIn(t *testing.T) {
	svc, _, _ := newDirectoryService(t)
	_, err := svc.BeginDatasourceAccountLink(context.Background(), userJane, testOrg, "crawler", "")
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestGroupBindingIsTheAdministratorsExplicitChoice(t *testing.T) {
	svc, _, audit := newDirectoryService(t)
	b, err := svc.BindDatasourceGroup(context.Background(), userAdm, testOrg, "github", "acme/platform", teamEng)
	require.NoError(t, err)
	require.Equal(t, teamEng, b.TeamID)
	_, err = svc.BindDatasourceGroup(context.Background(), userAdm, testOrg, "github", "acme/platform", teamEng)
	require.Equal(t, codes.AlreadyExists, status.Code(err), "a group binds to one team at most")
	_, err = svc.BindDatasourceGroup(context.Background(), userAdm, testOrg, "github", "acme/other", teamElse)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "another organization's team is not a target")
	_, err = svc.BindDatasourceGroup(context.Background(), userAdm, testOrg, "nope", "acme/x", teamEng)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.NoError(t, svc.UnbindDatasourceGroup(context.Background(), userAdm, testOrg, b.ID))
	require.Equal(t, codes.NotFound, status.Code(svc.UnbindDatasourceGroup(context.Background(), userAdm, testOrg, b.ID)))
	require.True(t, auditHas(audit, business.EventDatasourceGroupBound))
	require.True(t, auditHas(audit, business.EventDatasourceGroupUnbound))
	requireDeclaredPayloads(t, audit)
}

func TestDomainIsVerifiedByItsTXTRecord(t *testing.T) {
	svc, _, audit := newDirectoryService(t)
	d, err := svc.ClaimDatasourceDomain(context.Background(), userAdm, testOrg, " Example.COM. ")
	require.NoError(t, err)
	require.Equal(t, "example.com", d.Domain)
	require.Equal(t, "_saas-datasource-verification.example.com", d.TXTRecordName())
	require.True(t, strings.HasPrefix(d.TXTRecordValue(), "saas-datasource-verification="))

	txt := fakeTXT{}
	svc.SetDatasourceTXTResolver(txt)
	_, err = svc.VerifyDatasourceDomain(context.Background(), userAdm, testOrg, d.ID)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "no record yet")
	txt[d.TXTRecordName()] = []string{"saas-datasource-verification=someone-else"}
	_, err = svc.VerifyDatasourceDomain(context.Background(), userAdm, testOrg, d.ID)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "the wrong value proves nothing")
	txt[d.TXTRecordName()] = []string{"unrelated", d.TXTRecordValue()}
	v, err := svc.VerifyDatasourceDomain(context.Background(), userAdm, testOrg, d.ID)
	require.NoError(t, err)
	require.NotNil(t, v.VerifiedAt)

	_, err = svc.ClaimDatasourceDomain(context.Background(), userAdm, testOrg, "example.com")
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	for _, bad := range []string{"localhost", "-bad.example.com", "a..b", "exa mple.com"} {
		_, err = svc.ClaimDatasourceDomain(context.Background(), userAdm, testOrg, bad)
		require.Equal(t, codes.InvalidArgument, status.Code(err), bad)
	}
	require.NoError(t, svc.DeleteDatasourceDomain(context.Background(), userAdm, testOrg, d.ID))
	for _, e := range []business.EventType{business.EventDatasourceDomainClaimed, business.EventDatasourceDomainVerified, business.EventDatasourceDomainRemoved} {
		require.True(t, auditHas(audit, e), e)
	}
	requireDeclaredPayloads(t, audit)
}

// TestDirectoryTranslatesUnderTheSettledRules drives connector.Translate over
// the host's own directory: only the explicit mappings grant anything.
func TestDirectoryTranslatesUnderTheSettledRules(t *testing.T) {
	svc, _, _ := newDirectoryService(t)
	_, err := link(t, svc, userJane, "code-jane")
	require.NoError(t, err)
	_, err = svc.BindDatasourceGroup(context.Background(), userAdm, testOrg, "github", "acme/platform", teamEng)
	require.NoError(t, err)
	d, err := svc.ClaimDatasourceDomain(context.Background(), userAdm, testOrg, "example.com")
	require.NoError(t, err)
	svc.SetDatasourceTXTResolver(fakeTXT{d.TXTRecordName(): {d.TXTRecordValue()}})
	_, err = svc.VerifyDatasourceDomain(context.Background(), userAdm, testOrg, d.ID)
	require.NoError(t, err)
	_, err = svc.ClaimDatasourceDomain(context.Background(), userAdm, testOrg, "example.org") // pending
	require.NoError(t, err)

	dir := svc.DatasourceDirectoryFor(testOrg)
	readers, err := connector.Translate(context.Background(), dir, "github", testOrg, connector.ProviderACL{Entries: []connector.ProviderPrincipal{
		{Kind: connector.PrincipalAccount, ID: "1001"},            // linked: Jane
		{Kind: connector.PrincipalAccount, ID: "1002"},            // Joe never linked
		{Kind: connector.PrincipalGroup, ID: "acme/platform"},     // bound
		{Kind: connector.PrincipalGroup, ID: "acme/unbound"},      // grants nothing
		{Kind: connector.PrincipalDomain, ID: "Example.com"},      // verified
		{Kind: connector.PrincipalDomain, ID: "example.org"},      // pending
		{Kind: connector.PrincipalGuest, ID: "guest@example.net"}, // dropped
	}})
	require.NoError(t, err)
	require.Equal(t, []string{userJane}, readers.GetUserIds())
	require.Equal(t, []string{teamEng}, readers.GetGroupIds())
	require.True(t, readers.GetBoundaryReaders())
	require.EqualValues(t, 4, readers.GetUnmappedCount())

	// Another organization's directory knows none of it.
	other := svc.DatasourceDirectoryFor("88888888-8888-8888-8888-888888888888")
	readers, err = connector.Translate(context.Background(), other, "github", "88888888-8888-8888-8888-888888888888",
		connector.ProviderACL{Entries: []connector.ProviderPrincipal{{Kind: connector.PrincipalAccount, ID: "1001"}}})
	require.NoError(t, err)
	require.Equal(t, gen.DatasourceItemReadersBasis_DATASOURCE_ITEM_READERS_BASIS_UNTRANSLATABLE, readers.GetBasis())
}

// TestBudgetServesAPersonBeforeBackgroundWork holds the scheduler to the rule:
// background work spends at most its share of a credential's window, a person's
// sync the rest, and a provider rate limit stops both until it resets.
func TestBudgetServesAPersonBeforeBackgroundWork(t *testing.T) {
	svc, store, _ := newDirectoryService(t)
	svc.SetDatasourceBudgetStore(store)
	reg := svc.DatasourceConnectors()
	conn, ok := reg.Files("github")
	require.True(t, ok)
	budget := conn.Descriptor().Budget
	require.Equal(t, 80, budget.BackgroundSharePercent)

	source := githubSource(t, svc, "main", nil, "")
	background := context.Background()
	interactive := connector.WithPriority(background, connector.PriorityInteractive)
	src := connector.Source{ID: source.ID, OrgID: source.OrgID, CredentialKey: "github:source:" + source.ID,
		Config: github.SourceConfig{Repo: source.Repo, Branch: "main", InScope: func(string) bool { return true }}}

	share := budget.OperationsPerWindow * budget.BackgroundSharePercent / 100
	store.budgets[src.CredentialKey] = &budgetRow{started: time.Now(), used: share}
	_, err := conn.Version(background, src)
	var limited *connector.RateLimitedError
	require.ErrorAs(t, err, &limited, "background work yields once its share is spent")
	require.True(t, limited.Yielded)
	require.True(t, limited.ResetAt.After(time.Now()))
	_, err = conn.Version(interactive, src)
	require.NoError(t, err, "a person's sync is still served")

	store.budgets[src.CredentialKey].used = budget.OperationsPerWindow
	_, err = conn.Version(interactive, src)
	require.ErrorAs(t, err, &limited, "the whole window spent refuses even a person")

	// A window that has elapsed opens a new one.
	store.budgets[src.CredentialKey].started = time.Now().Add(-2 * budget.Window)
	_, err = conn.Version(background, src)
	require.NoError(t, err)

	// A provider block stops everything until it lifts.
	require.NoError(t, store.BlockDatasourceBudget(background, src.CredentialKey, time.Now().Add(time.Hour)))
	_, err = conn.Version(interactive, src)
	require.ErrorAs(t, err, &limited)
}

func TestBudgetRefusalHoldsTheJobUntilTheReset(t *testing.T) {
	reset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	failure := business.DatasourceProcessingErrorForTest(&connector.RateLimitedError{ResetAt: reset, Scope: "credential", Yielded: true})
	require.True(t, failure.Retryable)
	require.Equal(t, "datasource.budget_exhausted", failure.Failure.GetCode())
	require.True(t, failure.NotBefore.Equal(reset))
}

func TestCredentialKeysShareWhatTheProviderShares(t *testing.T) {
	cases := map[string]*business.DatasourceSource{
		"github:installation:42": {ID: "s1", Provider: "github", GitHubInstallationID: "42"},
		"github:public":          {ID: "s2", Provider: "github", GitHubCredentialKind: "public"},
		"github:source:s3":       {ID: "s3", Provider: "github"},
	}
	for want, src := range cases {
		require.Equal(t, want, business.DatasourceCredentialKeyForTest(src))
	}
}
