//go:build playground

// The host-only playground journeys that need no solution
// (module/PLAYGROUND.md), against a host Codefly is running:
//
//	codefly run service --fixture dev-admin     # from the repository root
//	make playground-journeys                    # this suite
//
// Every journey runs as a plain person holding only explicit grants. A
// journey that passes as an administrator proves nothing about its grant
// path, so the administrators here are cases under test (they must get
// nothing for free); the two steps that need one say why.
//
// No address is typed, exported or handed to another process: this test asks
// the Codefly CLI where the run's frontend is, and makes every call itself.
// PLAYGROUND_SCOPE names the run when it was started under a naming scope;
// CODEFLY names the CLI when it is not `codefly` on PATH.
package playground

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	collectionRole   = "Collection reader"
	collectionType   = "example-records"
	byGrant          = "ACCESS_BASIS_GRANT"
	accountsServices = "/saas.accounts.v1."
)

// playground is the running host: where its frontend answers, the fixture it
// was started with, and the people already signed in.
type playground struct {
	origin  string
	fixture *Fixture
	home    string
	client  *http.Client
	tokens  map[string]string
}

// origin asks the CLI for the frontend of the run in the repository's
// workspace, and fails unless it is up.
func origin(t *testing.T) string {
	t.Helper()
	cli := os.Getenv("CODEFLY")
	if cli == "" {
		cli = "codefly"
	}
	args := []string{"endpoint", "saas-starter/frontend", "--require-up"}
	if scope := os.Getenv("PLAYGROUND_SCOPE"); scope != "" {
		args = append(args, "--naming-scope", scope)
	}
	command := exec.Command(cli, args...)
	command.Dir = "../../.." // the repository root, whose workspace the run belongs to
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		t.Fatalf("no playground host is up (start one with `codefly run service --fixture dev-admin`): %v\n%s", err, stderr.String())
	}
	return strings.TrimRight(strings.TrimSpace(string(out)), "/")
}

func start(t *testing.T) *playground {
	t.Helper()
	fixture, err := Load(FixturePath)
	if err != nil {
		t.Fatal(err)
	}
	home, err := fixture.Organization(Home)
	if err != nil {
		t.Fatal(err)
	}
	return &playground{
		origin: origin(t), fixture: fixture, home: home,
		client: &http.Client{Timeout: 30 * time.Second}, tokens: map[string]string{},
	}
}

func (p *playground) post(path, bearer string, request any) (int, []byte, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return 0, nil, err
	}
	call, err := http.NewRequest(http.MethodPost, p.origin+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	call.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		call.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := p.client.Do(call)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	return response.StatusCode, answer, err
}

// token signs a fixture person in as the sign-in page does.
func (p *playground) token(t *testing.T, providerID string) string {
	t.Helper()
	if token := p.tokens[providerID]; token != "" {
		return token
	}
	person, err := p.fixture.Person(providerID)
	if err != nil {
		t.Fatal(err)
	}
	status, answer, err := p.post("/v1/auth/authenticate", "", map[string]any{
		"provider": "email", "fixture": map[string]string{"token": person.ProviderID},
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("sign in as %s: %d %s %v", providerID, status, answer, err)
	}
	var session struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(answer, &session); err != nil || session.AccessToken == "" {
		t.Fatalf("sign in as %s answered no access token: %s", providerID, answer)
	}
	p.tokens[providerID] = session.AccessToken
	return session.AccessToken
}

// call makes one accounts call as a fixture person and decodes its answer.
func (p *playground) call(t *testing.T, providerID, method string, request, answer any) error {
	t.Helper()
	status, raw, err := p.post(accountsServices+method, p.token(t, providerID), request)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s as %s: %d %s", method, providerID, status, raw)
	}
	if answer == nil {
		return nil
	}
	return json.Unmarshal(raw, answer)
}

// reads is the basis of each scope node of the example collection the person
// may read: empty when they read nothing.
func (p *playground) reads(t *testing.T, providerID string) []string {
	t.Helper()
	var answer struct {
		Scopes []struct {
			Basis string `json:"basis"`
		} `json:"scopes"`
	}
	if err := p.call(t, providerID, "AccessibleScopeService/ListMyAccessibleScopes", map[string]string{
		"orgId": p.home, "resourceType": collectionType, "action": "read",
	}, &answer); err != nil {
		t.Fatal(err)
	}
	bases := make([]string, 0, len(answer.Scopes))
	for _, scope := range answer.Scopes {
		bases = append(bases, scope.Basis)
	}
	return bases
}

func (p *playground) readsNothing(t *testing.T, providerID string) {
	t.Helper()
	if got := p.reads(t, providerID); len(got) != 0 {
		t.Errorf("%s reads the collection (%v) and holds no grant", providerID, got)
	}
}

func (p *playground) readsByGrant(t *testing.T, providerID string) {
	t.Helper()
	got := p.reads(t, providerID)
	if len(got) == 0 {
		t.Errorf("%s holds a grant and reads nothing", providerID)
	}
	for _, basis := range got {
		if basis != byGrant {
			t.Errorf("%s reads on %s, not on a grant", providerID, basis)
		}
	}
}

// grantOn is the request that gives, or takes back, the collection role at
// the example collection for one person. Reading the role and the collection
// is the collection owner's, so it is done as the owner.
func (p *playground) grantOn(t *testing.T, providerID string) map[string]string {
	t.Helper()
	var roles struct {
		Roles []struct{ ID, Name string } `json:"roles"`
	}
	if err := p.call(t, Supporting.Owner, "PermissionService/ListRoles", map[string]string{"orgId": p.home}, &roles); err != nil {
		t.Fatal(err)
	}
	role := ""
	for _, candidate := range roles.Roles {
		if candidate.Name == collectionRole {
			role = candidate.ID
		}
	}
	var access struct {
		Collections []struct {
			Node struct {
				ScopePath string `json:"scopePath"`
			} `json:"node"`
		} `json:"collections"`
	}
	if err := p.call(t, Supporting.Owner, "PermissionService/ListCollectionAccess", map[string]string{"orgId": p.home}, &access); err != nil {
		t.Fatal(err)
	}
	if role == "" || len(access.Collections) == 0 {
		t.Fatalf("the host seeds no %q role or no collection", collectionRole)
	}
	person, err := p.fixture.Person(providerID)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"orgId": p.home, "subjectId": person.ID, "subjectKind": "SUBJECT_KIND_PRINCIPAL",
		"scopePath": access.Collections[0].Node.ScopePath, "roleId": role,
	}
}

func TestAPersonReadsTheCollectionOnlyByAnExplicitGrant(t *testing.T) {
	p := start(t)
	p.readsByGrant(t, Supporting.Reader)
	// The three page-role cases hold no grant: none reads.
	for _, person := range SignIn {
		p.readsNothing(t, person)
	}
	// Administering the organization is not a grant.
	for _, administrator := range Supporting.Administrators {
		p.readsNothing(t, administrator)
	}
}

// Granting and revoking is an act of the collection's owner, so those two
// steps run as the owner: that is the one place an administrator is the lens.
func TestAccessFollowsTheGrantAndLeavesWithIt(t *testing.T) {
	p := start(t)
	grant := p.grantOn(t, Supporting.Grantee)
	p.readsNothing(t, Supporting.Grantee)
	// Registered first: a grant that lands while its reply is lost is still
	// taken back.
	revoked := false
	revoke := func() {
		if revoked {
			return
		}
		if err := p.call(t, Supporting.Owner, "PermissionService/RevokeScope", grant, nil); err != nil {
			t.Errorf("the grant to %s could not be revoked; revoke it by hand (RevokeScope): %v", Supporting.Grantee, err)
			return
		}
		revoked = true
	}
	t.Cleanup(revoke)
	if err := p.call(t, Supporting.Owner, "PermissionService/GrantScope", grant, nil); err != nil {
		t.Fatal(err)
	}
	p.readsByGrant(t, Supporting.Grantee)
	revoke()
	p.readsNothing(t, Supporting.Grantee)
}

// The platform super_admin is also the collection's owner here. With her own
// grant removed she reads nothing: a platform role is not a read. Restored
// after.
func TestAPlatformRoleIsNotARead(t *testing.T) {
	p := start(t)
	own := p.grantOn(t, Supporting.Owner)
	t.Cleanup(func() {
		if err := p.call(t, Supporting.Owner, "PermissionService/GrantScope", own, nil); err != nil {
			t.Errorf("the owner's grant could not be restored; GrantScope it by hand: %v", err)
		}
	})
	if err := p.call(t, Supporting.Owner, "PermissionService/RevokeScope", own, nil); err != nil {
		t.Fatal(err)
	}
	p.readsNothing(t, Supporting.Owner)
	if err := p.call(t, Supporting.Owner, "PermissionService/GrantScope", own, nil); err != nil {
		t.Fatal(err)
	}
	p.readsByGrant(t, Supporting.Owner)
}

func TestAPlatformRoleReadsNoOtherOrganizationsAuditTrail(t *testing.T) {
	p := start(t)
	elsewhere, err := p.fixture.Organization(Elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	status, answer, err := p.post(accountsServices+"AuditService/QueryAuditLog", p.token(t, Supporting.Owner),
		map[string]any{"orgId": elsewhere, "pageSize": 1})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusForbidden {
		t.Errorf("the platform super_admin read another organization's audit trail: %d %s", status, answer)
	}
}
