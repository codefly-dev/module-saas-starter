package business

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"accounts/pkg/datasource/connector"
	"accounts/pkg/datasource/github"
	"accounts/pkg/githubconnector"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The datasource directory: what the host knows about provider principals, so
// a connector that translates a provider's per-item access lists can name host
// users and teams. Every mapping is one somebody made on purpose, under the
// owner's settled rules:
//
//   - a person links their own provider account by signing in to the provider
//     as it; an account is never matched by email;
//   - an administrator binds a provider group to one of the organization's
//     teams; an unbound group grants nothing;
//   - an administrator claims a domain and proves it with a DNS TXT record;
//     only a verified domain lets "anyone in the domain" mean anything.
//
// Directory (below) serves those answers to connector.Translate.

// DatasourceAccountLink is one person's provider account.
type DatasourceAccountLink struct {
	ID                   string
	OrgID                string
	UserID               string
	Connector            string
	ProviderAccountID    string
	ProviderAccountLogin string
	CreatedAt            time.Time
}

// DatasourceGroupBinding maps one provider group onto one host team.
type DatasourceGroupBinding struct {
	ID              string
	OrgID           string
	Connector       string
	ProviderGroupID string
	TeamID          string
	CreatedBy       string
	CreatedAt       time.Time
}

// DatasourceDomain is a domain an administrator claimed for the organization.
// VerifiedAt is nil until its TXT record has been found.
type DatasourceDomain struct {
	ID                string
	OrgID             string
	Domain            string
	VerificationToken string
	VerifiedAt        *time.Time
	CreatedBy         string
	CreatedAt         time.Time
}

// TXTRecordName is where the domain's verification record lives.
func (d *DatasourceDomain) TXTRecordName() string { return datasourceDomainRecordPrefix + d.Domain }

// TXTRecordValue is what the record must carry.
func (d *DatasourceDomain) TXTRecordValue() string {
	return datasourceDomainValuePrefix + d.VerificationToken
}

// DatasourceDirectory is an organization's whole directory, as an administrator
// reads it, with the teams a group may be bound to.
type DatasourceDirectory struct {
	Links    []*DatasourceAccountLink
	Bindings []*DatasourceGroupBinding
	Domains  []*DatasourceDomain
	Teams    []DatasourceDirectoryTeam
}

// DatasourceDirectoryTeam is a bindable team.
type DatasourceDirectoryTeam struct {
	ID   string
	Name string
}

// DatasourceAccountLinkHandle is where to send the browser to sign in to the
// provider, and the state it will echo back.
type DatasourceAccountLinkHandle struct {
	AuthorizeURL string
	State        string
	ExpiresAt    time.Time
}

const (
	datasourceDomainRecordPrefix = "_saas-datasource-verification."
	datasourceDomainValuePrefix  = "saas-datasource-verification="

	// datasourceAccountLinkStateTTL bounds how long a sign-in may take.
	datasourceAccountLinkStateTTL = 15 * time.Minute
	// datasourceAccountLinkStatePrefix marks a link state apart from the GitHub
	// App setup state, which returns to the same kind of page.
	datasourceAccountLinkStatePrefix = "dl."
)

var (
	// ErrDatasourceAccountLinkedElsewhere: the provider account is already
	// linked to another person of the organization.
	ErrDatasourceAccountLinkedElsewhere = errors.New("datasource: that provider account is already linked to another person in this organization")
	// ErrDatasourceGroupAlreadyBound: the provider group is bound to a team.
	ErrDatasourceGroupAlreadyBound = errors.New("datasource: that provider group is already bound to a team")
	// ErrDatasourceDomainAlreadyClaimed: the organization already claimed it.
	ErrDatasourceDomainAlreadyClaimed = errors.New("datasource: that domain is already claimed by this organization")
)

// datasourceDomainPattern is the lower-case domain form the store accepts.
var datasourceDomainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// TXTResolver looks up a name's TXT records. The host uses the system
// resolver; tests inject one.
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// SetDatasourceTXTResolver overrides how domain verification reads DNS.
func (s *Service) SetDatasourceTXTResolver(r TXTResolver) { s.datasourceTXTResolver = r }

func (s *Service) txtResolver() TXTResolver {
	if s.datasourceTXTResolver != nil {
		return s.datasourceTXTResolver
	}
	return net.DefaultResolver
}

// DatasourceAccountLinker proves which provider account a person controls: it
// sends them to sign in and reads the account back from the provider's code.
type DatasourceAccountLinker interface {
	AuthorizeURL(state, redirectURI string) string
	ResolveAccount(ctx context.Context, code string) (id, login string, err error)
}

// datasourceAccountLinker is the linker for a connector, or nil when this
// deployment cannot prove accounts of that provider.
func (s *Service) datasourceAccountLinker(connectorKey string) DatasourceAccountLinker {
	if s.datasourceLinkers != nil {
		if l, ok := s.datasourceLinkers[connectorKey]; ok {
			return l
		}
		return nil
	}
	if connectorKey == github.ConnectorKey && s.githubConnector != nil && s.githubAppClientID != "" && s.githubAppClientSecret != "" {
		return githubAccountLinker{s: s}
	}
	return nil
}

// SetDatasourceAccountLinkers replaces the linkers by connector key. Tests use it.
func (s *Service) SetDatasourceAccountLinkers(linkers map[string]DatasourceAccountLinker) {
	s.datasourceLinkers = linkers
}

// githubAccountLinker proves a GitHub account through the deployment's App's
// user authorization: the person signs in to GitHub, and the code GitHub hands
// back is traded for a user token that says who they are.
type githubAccountLinker struct{ s *Service }

func (g githubAccountLinker) AuthorizeURL(state, redirectURI string) string {
	q := url.Values{"client_id": {g.s.githubAppClientID}, "state": {state}}
	if redirectURI != "" {
		q.Set("redirect_uri", redirectURI)
	}
	return githubAppInstallBaseURL + "/login/oauth/authorize?" + q.Encode()
}

func (g githubAccountLinker) ResolveAccount(ctx context.Context, code string) (string, string, error) {
	token, err := g.s.githubConnector.ExchangeUserCode(ctx, g.s.githubAppClientID, g.s.githubAppClientSecret, code)
	if err != nil {
		return "", "", err
	}
	return g.s.githubConnector.AuthenticatedUser(ctx, token)
}

// datasourceLinkClaims is the signed body of a link state: it binds the sign-in
// to one organization, one person and one connector, for a short while.
type datasourceLinkClaims struct {
	OrgID     string `json:"o"`
	UserID    string `json:"u"`
	Connector string `json:"c"`
	ExpiresAt int64  `json:"e"`
	Nonce     string `json:"n"`
}

func (s *Service) signDatasourceLinkState(claims datasourceLinkClaims) (string, error) {
	if len(s.datasourceLinkKey) == 0 {
		return "", errors.New("datasource link state key is not configured")
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.datasourceLinkKey)
	mac.Write([]byte(encoded))
	return datasourceAccountLinkStatePrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) verifyDatasourceLinkState(state string, now time.Time) (*datasourceLinkClaims, error) {
	if len(s.datasourceLinkKey) == 0 {
		return nil, errors.New("datasource link state key is not configured")
	}
	rest, ok := strings.CutPrefix(state, datasourceAccountLinkStatePrefix)
	if !ok {
		return nil, errDatasourceLinkStateRejected
	}
	encoded, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, errDatasourceLinkStateRejected
	}
	mac := hmac.New(sha256.New, s.datasourceLinkKey)
	mac.Write([]byte(encoded))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return nil, errDatasourceLinkStateRejected
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errDatasourceLinkStateRejected
	}
	var claims datasourceLinkClaims
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, errDatasourceLinkStateRejected
	}
	if now.Unix() >= claims.ExpiresAt {
		return nil, errDatasourceLinkStateRejected
	}
	return &claims, nil
}

var errDatasourceLinkStateRejected = errors.New("datasource: link state rejected")

// BeginDatasourceAccountLink returns where the calling person signs in to the
// provider to prove which account is theirs.
func (s *Service) BeginDatasourceAccountLink(ctx context.Context, actorID, orgID, connectorKey, redirectURI string) (*DatasourceAccountLinkHandle, error) {
	linker := s.datasourceAccountLinker(connectorKey)
	if linker == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "This deployment cannot link %s accounts: no sign-in with that provider is configured.", connectorKey)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, status.Error(codes.Internal, "Could not start the account link. Retry shortly.")
	}
	expires := time.Now().UTC().Add(datasourceAccountLinkStateTTL)
	state, err := s.signDatasourceLinkState(datasourceLinkClaims{
		OrgID: orgID, UserID: actorID, Connector: connectorKey, ExpiresAt: expires.Unix(),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	})
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "This deployment cannot link accounts: its link signing key is not configured.")
	}
	s.emit(ctx, actorID, "user", EventDatasourceAccountLinkStarted, "organization", orgID, orgID,
		map[string]any{"connector": connectorKey})
	return &DatasourceAccountLinkHandle{AuthorizeURL: linker.AuthorizeURL(state, redirectURI), State: state, ExpiresAt: expires}, nil
}

// CompleteDatasourceAccountLink redeems a link state for the person who began
// it and links the provider account the code signs in as. A provider account
// already linked to another person of the organization is refused: linking it
// would hand that person's provider access to someone else.
func (s *Service) CompleteDatasourceAccountLink(ctx context.Context, actorID, orgID, state, code string) (*DatasourceAccountLink, error) {
	claims, err := s.verifyDatasourceLinkState(state, time.Now().UTC())
	if err != nil || claims.OrgID != orgID || claims.UserID != actorID {
		return nil, status.Error(codes.PermissionDenied, "This account link is no longer valid. Start it again.")
	}
	linker := s.datasourceAccountLinker(claims.Connector)
	if linker == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "This deployment cannot link %s accounts.", claims.Connector)
	}
	accountID, login, err := linker.ResolveAccount(ctx, code)
	if err != nil {
		if errors.Is(err, githubconnector.ErrUserCodeRejected) {
			return nil, status.Error(codes.PermissionDenied, "The provider did not accept that sign-in. Start the account link again.")
		}
		return nil, status.Error(codes.Unavailable, "Could not read the signed-in account from the provider. Retry shortly.")
	}
	if strings.TrimSpace(accountID) == "" {
		return nil, status.Error(codes.PermissionDenied, "The provider did not say which account signed in.")
	}
	link := &DatasourceAccountLink{
		ID: NewIDString(), OrgID: orgID, UserID: actorID, Connector: claims.Connector,
		ProviderAccountID: accountID, ProviderAccountLogin: login,
	}
	var stored *DatasourceAccountLink
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		out, err := s.store.UpsertDatasourceAccountLink(ctx, link)
		if err != nil {
			return err
		}
		stored = out
		return s.emitTx(ctx, actorID, "user", EventDatasourceAccountLinked, "datasource_account_link", out.ID, orgID,
			map[string]any{"connector": out.Connector, "provider_account_id": out.ProviderAccountID})
	}); err != nil {
		if errors.Is(err, ErrDatasourceAccountLinkedElsewhere) {
			return nil, status.Error(codes.AlreadyExists, "That provider account is already linked to another person in this organization.")
		}
		return nil, err
	}
	return stored, nil
}

// ListMyDatasourceAccountLinks returns the calling person's links.
func (s *Service) ListMyDatasourceAccountLinks(ctx context.Context, actorID, orgID string) ([]*DatasourceAccountLink, error) {
	var out []*DatasourceAccountLink
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		links, err := s.store.ListDatasourceAccountLinks(ctx, orgID, actorID)
		out = links
		return err
	})
	return out, err
}

// DeleteDatasourceAccountLink removes a link. A person may remove their own;
// removing someone else's takes an administrator of the organization.
func (s *Service) DeleteDatasourceAccountLink(ctx context.Context, actorID, orgID, id string) error {
	return s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		link, err := s.store.GetDatasourceAccountLink(ctx, orgID, id)
		if err != nil {
			return err
		}
		if link == nil {
			return status.Error(codes.NotFound, "account link not found")
		}
		if link.UserID != actorID {
			membership, err := s.store.GetOrgMembership(ctx, orgID, actorID)
			if err != nil {
				return err
			}
			if membership == nil || !IsOrgAdminRole(orgRoleToString(membership.GetRole())) {
				// Indistinguishable from absent: someone else's link is not
				// this caller's to learn about.
				return status.Error(codes.NotFound, "account link not found")
			}
		}
		if err := s.store.DeleteDatasourceAccountLink(ctx, orgID, id); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceAccountUnlinked, "datasource_account_link", id, orgID,
			map[string]any{"connector": link.Connector, "provider_account_id": link.ProviderAccountID, "user_id": link.UserID})
	})
}

// GetDatasourceDirectory returns the organization's whole directory.
func (s *Service) GetDatasourceDirectory(ctx context.Context, orgID string) (*DatasourceDirectory, error) {
	out := &DatasourceDirectory{}
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if out.Links, err = s.store.ListDatasourceAccountLinks(ctx, orgID, ""); err != nil {
			return err
		}
		if out.Bindings, err = s.store.ListDatasourceGroupBindings(ctx, orgID); err != nil {
			return err
		}
		if out.Domains, err = s.store.ListDatasourceDomains(ctx, orgID); err != nil {
			return err
		}
		teams, err := s.store.ListTeams(ctx, orgID, "")
		if err != nil {
			return err
		}
		for _, t := range teams {
			out.Teams = append(out.Teams, DatasourceDirectoryTeam{ID: t.GetId(), Name: t.GetName()})
		}
		return nil
	})
	return out, err
}

// BindDatasourceGroup maps a provider group onto one of the organization's
// teams. A group is bound to one team at most.
func (s *Service) BindDatasourceGroup(ctx context.Context, actorID, orgID, connectorKey, groupID, teamID string) (*DatasourceGroupBinding, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, status.Error(codes.InvalidArgument, "provider group id is required")
	}
	if s.datasourceConnectors != nil {
		if _, ok := s.datasourceConnectors.Descriptor(connectorKey); !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unknown datasource connector %q", connectorKey)
		}
	}
	binding := &DatasourceGroupBinding{ID: NewIDString(), OrgID: orgID, Connector: connectorKey, ProviderGroupID: groupID, TeamID: teamID, CreatedBy: actorID}
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		teamOrg, err := s.store.GetTeamOrgID(ctx, teamID)
		if err != nil || teamOrg != orgID {
			return status.Error(codes.InvalidArgument, "that team is not a team of this organization")
		}
		if err := s.store.InsertDatasourceGroupBinding(ctx, binding); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceGroupBound, "datasource_group_binding", binding.ID, orgID,
			map[string]any{"connector": connectorKey, "provider_group_id": groupID, "team_id": teamID})
	})
	if errors.Is(err, ErrDatasourceGroupAlreadyBound) {
		return nil, status.Error(codes.AlreadyExists, "That provider group is already bound to a team. Unbind it first.")
	}
	if err != nil {
		return nil, err
	}
	binding.CreatedAt = time.Now().UTC()
	return binding, nil
}

// UnbindDatasourceGroup removes a binding; the group then grants nothing.
func (s *Service) UnbindDatasourceGroup(ctx context.Context, actorID, orgID, id string) error {
	return s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		binding, err := s.store.DeleteDatasourceGroupBinding(ctx, orgID, id)
		if err != nil {
			return err
		}
		if binding == nil {
			return status.Error(codes.NotFound, "group binding not found")
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceGroupUnbound, "datasource_group_binding", id, orgID,
			map[string]any{"connector": binding.Connector, "provider_group_id": binding.ProviderGroupID, "team_id": binding.TeamID})
	})
}

// ClaimDatasourceDomain records a domain as pending and returns the TXT record
// that proves the organization controls it.
func (s *Service) ClaimDatasourceDomain(ctx context.Context, actorID, orgID, domain string) (*DatasourceDomain, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if !datasourceDomainPattern.MatchString(domain) || len(domain) > 253 {
		return nil, status.Error(codes.InvalidArgument, "domain must be a DNS name such as example.com")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, status.Error(codes.Internal, "Could not claim the domain. Retry shortly.")
	}
	d := &DatasourceDomain{ID: NewIDString(), OrgID: orgID, Domain: domain, VerificationToken: base64.RawURLEncoding.EncodeToString(raw), CreatedBy: actorID}
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.store.InsertDatasourceDomain(ctx, d); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceDomainClaimed, "datasource_domain", d.ID, orgID,
			map[string]any{"domain": domain})
	})
	if errors.Is(err, ErrDatasourceDomainAlreadyClaimed) {
		return nil, status.Error(codes.AlreadyExists, "This organization has already claimed that domain.")
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt = time.Now().UTC()
	return d, nil
}

// VerifyDatasourceDomain reads the domain's TXT record and marks it verified
// when it carries the expected value. A record not found yet is not an error:
// the domain stays pending and the caller may try again once DNS has spread.
func (s *Service) VerifyDatasourceDomain(ctx context.Context, actorID, orgID, id string) (*DatasourceDomain, error) {
	var d *DatasourceDomain
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		d, err = s.store.GetDatasourceDomain(ctx, orgID, id)
		return err
	}); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, status.Error(codes.NotFound, "domain not found")
	}
	if d.VerifiedAt != nil {
		return d, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	records, err := s.txtResolver().LookupTXT(lookupCtx, d.TXTRecordName())
	var dnsErr *net.DNSError
	if err != nil && (!errors.As(err, &dnsErr) || !dnsErr.IsNotFound) {
		return nil, status.Error(codes.Unavailable, "Could not read the domain's DNS. Retry shortly.")
	}
	found := false
	for _, r := range records {
		if strings.TrimSpace(r) == d.TXTRecordValue() {
			found = true
			break
		}
	}
	if !found {
		return nil, status.Errorf(codes.FailedPrecondition,
			"The TXT record %s does not carry the expected value yet. Publish it, wait for DNS to update, then verify again.", d.TXTRecordName())
	}
	now := time.Now().UTC()
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		if err := s.store.MarkDatasourceDomainVerified(ctx, orgID, id, now); err != nil {
			return err
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceDomainVerified, "datasource_domain", id, orgID,
			map[string]any{"domain": d.Domain})
	}); err != nil {
		return nil, err
	}
	d.VerifiedAt = &now
	return d, nil
}

// DeleteDatasourceDomain removes a claimed domain.
func (s *Service) DeleteDatasourceDomain(ctx context.Context, actorID, orgID, id string) error {
	return s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		d, err := s.store.DeleteDatasourceDomain(ctx, orgID, id)
		if err != nil {
			return err
		}
		if d == nil {
			return status.Error(codes.NotFound, "domain not found")
		}
		return s.emitTx(ctx, actorID, "user", EventDatasourceDomainRemoved, "datasource_domain", id, orgID,
			map[string]any{"domain": d.Domain})
	})
}

// DatasourceDirectoryFor is the connector.Directory over one organization's
// directory, for connector.Translate. Each question is answered in one query.
func (s *Service) DatasourceDirectoryFor(orgID string) connector.Directory {
	return datasourceDirectory{s: s, orgID: orgID}
}

type datasourceDirectory struct {
	s     *Service
	orgID string
}

func (d datasourceDirectory) LinkedUsers(ctx context.Context, connectorKey string, accountIDs []string) (map[string]string, error) {
	var out map[string]string
	err := d.s.store.WithOrgTx(ctx, d.orgID, func(ctx context.Context) error {
		var err error
		out, err = d.s.store.LinkedDatasourceUsers(ctx, d.orgID, connectorKey, accountIDs)
		return err
	})
	return out, err
}

func (d datasourceDirectory) BoundTeams(ctx context.Context, connectorKey string, groupIDs []string) (map[string]string, error) {
	var out map[string]string
	err := d.s.store.WithOrgTx(ctx, d.orgID, func(ctx context.Context) error {
		var err error
		out, err = d.s.store.BoundDatasourceTeams(ctx, d.orgID, connectorKey, groupIDs)
		return err
	})
	return out, err
}

func (d datasourceDirectory) VerifiedDomains(ctx context.Context, orgID string, domains []string) (map[string]bool, error) {
	if orgID != d.orgID {
		return nil, fmt.Errorf("datasource directory of org %s asked about org %s", d.orgID, orgID)
	}
	lower := make([]string, len(domains))
	for i, dom := range domains {
		lower[i] = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(dom), "."))
	}
	var verified map[string]bool
	err := d.s.store.WithOrgTx(ctx, d.orgID, func(ctx context.Context) error {
		var err error
		verified, err = d.s.store.VerifiedDatasourceDomains(ctx, d.orgID, lower)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(domains))
	for i, dom := range domains {
		out[dom] = verified[lower[i]]
	}
	return out, nil
}
