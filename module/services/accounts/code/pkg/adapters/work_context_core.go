package adapters

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	carrier "github.com/codefly-dev/sdk-go/workcontext"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// The host on core/workcontext: the one implementation of the Work Context, for
// minting and verifying alike.
//
// Every capability this host issues is signed by core's Authority and every one
// it reads back is checked by core's Verifier. There is no second encoding and no
// window in which one is accepted beside the other: core refuses a token in any
// other format as ErrNotACoreToken before it reads a field.
//
// What the host owns is the four live answers core asks of an issuer — the seal
// (installation, epoch, execution), the authorization revision, the replay
// store and the approval grants — and it answers them from the same functions
// whether it is minting or verifying (business/work_context_seal.go), so the two
// can never disagree about what "current" means.

// workContextDefaultTTL is the window a mint gets when the request names none.
const workContextDefaultTTL = 5 * time.Minute

// workContextSealAnswers is what the host answers core's live questions from.
// business.Service implements it; a test supplies the answers it is about.
type workContextSealAnswers interface {
	WorkContextSeal(ctx context.Context, orgID, principalID, installationID string) (string, uint64, error)
	WorkContextPrincipalEpoch(ctx context.Context, orgID, principalID string) (uint64, error)
	WorkContextApprovedBuild(ctx context.Context, orgID, principalID string) (string, uint64, error)
	WorkContextRevision(ctx context.Context, orgID, ownerID string) (uint64, error)
	WorkContextAudienceInstallation(ctx context.Context, orgID, audience string) (string, error)
	WorkContextOperationBinding(ctx context.Context, orgID, bindingID string) (business.WorkContextOperationBinding, error)
}

// hostSealSource is core's SealSource for one organization.
//
// The organization is bound at construction, for the tenant the capability
// names, because every answer is per organization and core's interface carries
// none. The tenant is read from the capability being checked — but core consults
// a seal source only AFTER the signature, issuer, audience and window checks, so
// by then the tenant is the issuer's own statement, not a caller's.
type hostSealSource struct {
	answers workContextSealAnswers
	tenant  string
}

func (s hostSealSource) Seal(ctx context.Context, principalID, installationID string) (workcontext.Seal, error) {
	id, revision, err := s.answers.WorkContextSeal(ctx, s.tenant, principalID, installationID)
	switch {
	case errors.Is(err, business.ErrWorkContextSealUnheld), errors.Is(err, business.ErrModuleInstallationInactive):
		return workcontext.Seal{}, fmt.Errorf("%w: %w", workcontext.ErrNoSeal, err)
	case err != nil:
		return workcontext.Seal{}, err
	}
	return workcontext.Seal{InstallationID: id, InstallationRevision: revision}, nil
}

func (s hostSealSource) PrincipalEpoch(ctx context.Context, principalID string) (uint64, error) {
	epoch, err := s.answers.WorkContextPrincipalEpoch(ctx, s.tenant, principalID)
	if errors.Is(err, business.ErrWorkContextPrincipalUnknown) {
		return 0, fmt.Errorf("%w: %w", workcontext.ErrNoSeal, err)
	}
	return epoch, err
}

// ApprovedBuild translates the host's "bears no execution" into core's sentinel,
// and nothing else into it: every other error, a module principal with no
// approved build included, stays a refusal.
func (s hostSealSource) ApprovedBuild(ctx context.Context, principalID string) (string, uint64, error) {
	digest, incarnation, err := s.answers.WorkContextApprovedBuild(ctx, s.tenant, principalID)
	if errors.Is(err, business.ErrBearsNoExecution) {
		return "", 0, workcontext.ErrNoApprovedBuild
	}
	return digest, incarnation, err
}

// OperationBinding resolves one operation binding by its id, exactly: today the
// only kind is a source delegation used by one module
// (business.SourceDelegationBindingID). The binding's principal and installation
// come from the issuer's records, never from the capability, and core refuses a
// capability whose exercising principal or sealed installation is not the
// binding's — so a delegation presented by a module it does not entitle verifies
// nowhere.
func (s hostSealSource) OperationBinding(ctx context.Context, bindingID string) (workcontext.OperationBinding, error) {
	live, err := s.answers.WorkContextOperationBinding(ctx, s.tenant, bindingID)
	switch {
	case errors.Is(err, business.ErrNoOperationBinding):
		return workcontext.OperationBinding{}, fmt.Errorf("%w: %w", workcontext.ErrNoBinding, err)
	case err != nil:
		return workcontext.OperationBinding{}, err
	}
	return workcontext.OperationBinding{
		ID: live.ID, PrincipalID: live.PrincipalID, InstallationID: live.InstallationID,
		Revision: live.Revision, Incarnation: live.Incarnation, Revoked: live.Revoked,
	}, nil
}

var _ workcontext.SealSource = hostSealSource{}

// ownerRevisions answers the authorization revision of capabilities OWNED by
// one principal.
//
// Core's RevisionSource is keyed by tenant alone; this host's revision is the
// greater of the organization's and the owner's own (a module principal's is its
// installed grant), so the source is bound to the owner as well. The owner comes
// from the capability being checked, on the same terms as the tenant above, and
// at a mint it is the authenticated owner the capability is being issued to.
type ownerRevisions struct {
	answers workContextSealAnswers
	owner   string
}

func (r ownerRevisions) AuthorizationRevision(ctx context.Context, tenantID string) (uint64, error) {
	return r.answers.WorkContextRevision(ctx, tenantID, r.owner)
}

// tenantReplay is core's ReplayStore over the host's durable one, so a
// single-use capability is consumed once across every replica — and once
// whichever of this host and a consumer verifies it first.
type tenantReplay struct {
	store  business.WorkContextReplayStore
	tenant string
}

func (r tenantReplay) Consume(ctx context.Context, nonce string, retainUntil time.Time) error {
	if r.store == nil {
		return errors.New("no durable replay store is wired, so a single-use capability cannot be consumed")
	}
	err := r.store.ConsumeSingleUseWorkContext(ctx, r.tenant, nonce, retainUntil)
	if errors.Is(err, business.ErrWorkContextAlreadyConsumed) {
		return fmt.Errorf("%w: %w", workcontext.ErrReplayed, err)
	}
	return err
}

// noApprovalGrants is core's GrantSource for a host that records no approval
// grants: a capability carrying a grant hop names a decision this host never
// made, and is refused.
type noApprovalGrants struct{}

func (noApprovalGrants) Grant(context.Context, string) (*workcontext.Grant, error) {
	return nil, errors.New("this host records no approval grants")
}

// coreAuthority is the minter for capabilities owned by owner in tenant.
func (s *WorkContextAuthorityServer) coreAuthority(tenant, owner string) *workcontext.Authority {
	return &workcontext.Authority{
		Issuer:    s.issuer,
		KeyID:     s.keyID,
		Key:       s.key,
		Revisions: ownerRevisions{answers: s.seals, owner: owner},
		Seals:     hostSealSource{answers: s.seals, tenant: tenant},
		MaxTTL:    workcontext.DefaultMaxTTL,
		Now:       s.now,
	}
}

// coreVerifier is the callee check for capabilities owned by owner in tenant,
// presented as audience.
func (s *WorkContextAuthorityServer) coreVerifier(tenant, owner, audience string) *workcontext.Verifier {
	return &workcontext.Verifier{
		Issuer:    s.issuer,
		Audience:  audience,
		Keys:      map[string]ed25519.PublicKey{s.keyID: s.publicKey},
		Revisions: ownerRevisions{answers: s.seals, owner: owner},
		Replay:    tenantReplay{store: s.replay, tenant: tenant},
		Grants:    noApprovalGrants{},
		Seals:     hostSealSource{answers: s.seals, tenant: tenant},
		Now:       s.now,
	}
}

func (s *WorkContextAuthorityServer) configured() bool {
	return s != nil && s.configureErr == nil && s.key != nil && s.seals != nil
}

// verifyFor verifies a capability presented to this host AS audience: the
// module capability surface, the source operation surface. The full check —
// signature, issuer, audience, window, revision, seal, replay — at core's one
// strength.
func (s *WorkContextAuthorityServer) verifyFor(ctx context.Context, encoded, audience string) (*workcontext.Verified, error) {
	if !s.configured() {
		return nil, ErrWorkContextAuthorityUnconfigured
	}
	decoded, err := workcontext.Decode(encoded)
	if err != nil {
		return nil, err
	}
	claims := decoded.Context()
	return s.coreVerifier(claims.GetTenantId(), claims.GetOwnerPrincipalId(), audience).Verify(ctx, encoded)
}

// verifyIssued verifies a capability this host issued and is now asked to READ
// BACK on its holder's behalf: a parent presented to derive from, a viewer's
// capability presented with a question about what it may read.
//
// Here this host is the issuer, not the capability's consumer. The audience
// names the consumer the capability was minted FOR — which module's declaration
// governs it, which service it is being exchanged away from — so it is the
// subject of the question rather than this process's identity, and it is taken
// from the capability. Every other check runs at full strength: the signature
// under this issuer's key, the window, the live revision and seal, and replay.
// A single-use capability presented here is therefore SPENT here, exactly as it
// is by whichever consumer verifies it first.
//
// Where the party presenting a capability has an audience of its own, use
// verifyFor with that audience instead.
func (s *WorkContextAuthorityServer) verifyIssued(ctx context.Context, encoded string) (*workcontext.Verified, error) {
	if !s.configured() {
		return nil, ErrWorkContextAuthorityUnconfigured
	}
	decoded, err := workcontext.Decode(encoded)
	if err != nil {
		return nil, err
	}
	claims := decoded.Context()
	return s.coreVerifier(claims.GetTenantId(), claims.GetOwnerPrincipalId(), claims.GetAudience()).Verify(ctx, encoded)
}

// delegationHop is one actor hop a mint appends after the owner's session.
type delegationHop struct {
	PrincipalID   string
	PrincipalKind string
	AgentID       string
	DelegationID  string
	GrantedScopes []*basev0.WorkScopeV1
	// Execution is what this hop's principal is running, when it bears an
	// execution — a module principal, attested from its authenticated workload.
	// Empty for a person or a registered agent, which bear none.
	Execution workcontext.Execution
	// OperationBindingID is the unit of authority this hop exercises, when it
	// exercises one; its revision and incarnation are read live by core.
	OperationBindingID string
}

// mintRequest is one capability to issue: the owner's first session, and the
// hops delegated from it in order. The capability returned is the LAST one
// minted; the session, replay policy and window requested apply to it.
type mintRequest struct {
	Start workcontext.StartInput
	Hops  []delegationHop
	// Session is the session the returned capability opens: the caller's
	// verified session when the issuer has one to bind, empty for a fresh one.
	// Never a caller-named value — see core's ChildInput.SessionID for why a
	// pairing on the session holds only while that is true.
	Session string
	Replay  string
	TTL     time.Duration
}

// mint issues a capability through core's Authority.
//
// Core's Start carries no actor hop — the owner acting directly is not dressed up
// as a delegation to itself — so a delegated capability is the owner's session
// followed by a Child per hop, each verified before the next is derived from it
// (core derives only from a Verified parent). The intermediate capabilities are
// idempotent and never leave this function; the one returned carries the
// requested session, replay policy and window.
func (s *WorkContextAuthorityServer) mint(ctx context.Context, request mintRequest) (string, *basev0.WorkContextV1, error) {
	if !s.configured() {
		return "", nil, ErrWorkContextAuthorityUnconfigured
	}
	start := request.Start
	ttl := request.TTL
	if ttl == 0 {
		ttl = workContextDefaultTTL
	}
	authority := s.coreAuthority(start.TenantID, start.OwnerPrincipalID)
	if len(request.Hops) == 0 {
		start.SessionID = request.Session
		start.ReplayPolicy = request.Replay
		start.TTL = ttl
		return authority.Start(ctx, start)
	}
	start.SessionID = ""
	start.ReplayPolicy = workcontext.ReplayIdempotent
	start.TTL = ttl
	token, _, err := authority.Start(ctx, start)
	if err != nil {
		return "", nil, err
	}
	var signed *basev0.WorkContextV1
	for i, hop := range request.Hops {
		parent, err := s.coreVerifier(start.TenantID, start.OwnerPrincipalID, start.Audience).Verify(ctx, token)
		if err != nil {
			return "", nil, fmt.Errorf("verify the session a hop is derived from: %w", err)
		}
		child := workcontext.ChildInput{
			PrincipalID:   hop.PrincipalID,
			PrincipalKind: hop.PrincipalKind,
			AgentID:       hop.AgentID,
			DelegationID:  hop.DelegationID,
			GrantedScopes: hop.GrantedScopes,
			Audience:      start.Audience,
			ReplayPolicy:  workcontext.ReplayIdempotent,
			TTL:           ttl,
			Execution:     hop.Execution,
			// A hop naming none carries its parent's, which for the owner's
			// session is none.
			OperationBindingID: hop.OperationBindingID,
		}
		if i == len(request.Hops)-1 {
			child.SessionID = request.Session
			child.ReplayPolicy = request.Replay
		}
		token, signed, err = authority.Child(ctx, parent, child)
		if err != nil {
			return "", nil, err
		}
	}
	return token, signed, nil
}

// remint re-issues a verified capability for a new audience: the same owner,
// the same chain of hops with the same delegation ids, the last hop's scopes
// optionally attenuated. It is how an audience exchange and a renewal are
// expressed on core, which derives only by adding a hop and never extends a
// window: the capability is minted afresh from its owner, and every hop is
// re-derived and re-checked against live state rather than copied.
//
// lastExecution attests the last hop's execution when that hop's principal bears
// one; earlier hops cannot be attested by the caller and must bear none, so a
// chain with a module principal anywhere but last is refused by core.
func (s *WorkContextAuthorityServer) remint(
	ctx context.Context, parent *basev0.WorkContextV1, audience, replay string, ttl time.Duration,
	attenuated []*basev0.WorkScopeV1, lastExecution workcontext.Execution, session string,
) (string, *basev0.WorkContextV1, error) {
	// The seal follows the owner. A person's capability is sealed to the
	// organization's installation of the audience it addresses — what an
	// uninstall revokes — so a capability re-derived for an audience, renewed
	// included, is sealed to that audience's installation as it stands now and
	// is refused when the organization has none. Never the parent's: that names
	// another audience's installation, or one since replaced. A module or
	// installation principal is sealed to its own installation, which the
	// audience does not change.
	installation := parent.GetSeal().GetInstallationId()
	if parent.GetOwnerPrincipalKind() == business.PrincipalKindHuman {
		var err error
		if installation, err = s.audienceInstallation(ctx, parent.GetTenantId(), audience); err != nil {
			return "", nil, err
		}
	}
	start := workcontext.StartInput{
		TenantID:           parent.GetTenantId(),
		OwnerPrincipalID:   parent.GetOwnerPrincipalId(),
		OwnerPrincipalKind: parent.GetOwnerPrincipalKind(),
		OwnerAgentID:       parent.GetOwnerAgentId(),
		OrganizationID:     parent.GetOrganizationId(),
		TaskID:             parent.GetTaskId(),
		Audience:           audience,
		AuthorityScopes:    cloneWorkScopes(parent.GetAuthorityScopes()),
		AttributionTeamIDs: append([]string(nil), parent.GetAttributionTeamIds()...),
		WorkspaceID:        parent.GetWorkspaceId(),
		ProjectID:          parent.GetProjectId(),
		InstallationID:     installation,
	}
	chain := parent.GetActorChain()
	if len(chain) == 0 {
		if attenuated != nil {
			start.AuthorityScopes = attenuated
		}
		start.Execution = lastExecution
		return s.mint(ctx, mintRequest{Start: start, Session: session, Replay: replay, TTL: ttl})
	}
	hops := make([]delegationHop, 0, len(chain))
	for _, actor := range chain {
		hops = append(hops, delegationHop{
			PrincipalID:   actor.GetPrincipalId(),
			PrincipalKind: actor.GetPrincipalKind(),
			AgentID:       actor.GetAgentId(),
			DelegationID:  actor.GetDelegationId(),
			GrantedScopes: cloneWorkScopes(actor.GetGrantedScopes()),
		})
	}
	last := &hops[len(hops)-1]
	if attenuated != nil {
		last.GrantedScopes = attenuated
	}
	last.Execution = lastExecution
	last.OperationBindingID = parent.GetOperationBinding().GetBindingId()
	return s.mint(ctx, mintRequest{Start: start, Hops: hops, Session: session, Replay: replay, TTL: ttl})
}

// issuedSession is the session a capability opened by an authenticated caller
// carries: the caller's verified session when it has one, otherwise a fresh one
// minted by core. Never the id a request named.
func issuedSession(verified uuid.UUID, ok bool) string {
	if !ok {
		return ""
	}
	return verified.String()
}

// workContextHeader is the one HTTP carrier for a signed capability, owned by
// the SDK's carrier package rather than restated here.
const workContextHeader = carrier.HeaderName

// workContextGrants reports whether a verified capability's EFFECTIVE authority
// — the last hop's granted scopes, or the owner's when it acts directly — holds
// action on resourceKind, for resourceID when one is named. A scope naming no
// resource ids covers every resource of its kind.
//
// This is a host authorization question asked of claims core has already
// verified; it decodes and checks nothing about the capability itself.
func workContextGrants(claims *basev0.WorkContextV1, resourceKind, action, resourceID string) bool {
	effective := claims.GetAuthorityScopes()
	if chain := claims.GetActorChain(); len(chain) > 0 {
		effective = chain[len(chain)-1].GetGrantedScopes()
	}
	for _, scope := range effective {
		if scope.GetResourceKind() != resourceKind || !slices.Contains(scope.GetActions(), action) {
			continue
		}
		if len(scope.GetResourceIds()) == 0 || (resourceID != "" && slices.Contains(scope.GetResourceIds(), resourceID)) {
			return true
		}
	}
	return false
}

// refusedReadBack maps a refusal from verifying a capability this host issued.
// A capability the issuer's live state has moved past — a superseded revision, a
// moved seal or epoch — answers FailedPrecondition, the code the host's own
// recheck has always answered a stale capability with and the one a consumer
// re-mints on. Anything else answers invalid, with the site's own wording.
func refusedReadBack(err error, invalid error) error {
	if errors.Is(err, workcontext.ErrRevoked) || errors.Is(err, workcontext.ErrNoSeal) {
		return status.Error(codes.FailedPrecondition, "Work Context authorization revision is stale: its authority changed since it was minted")
	}
	return invalid
}
