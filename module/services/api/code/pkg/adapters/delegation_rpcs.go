package adapters

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/codefly-dev/core/policy"
	"github.com/codefly-dev/core/wool"

	"api/pkg/business"
	"api/pkg/gen"
)

// =====================================================================
// DelegationService RPCs (M7 escalation flow + M8 pattern grants)
// =====================================================================

// DelegationServer handles DelegationService RPCs. Lives in its
// own file — auto-generated grpc_gen.go should NOT have to know
// about this; the manual server registration in gen wiring picks
// it up by interface satisfaction.
//
// Holds a reference to the scoped-auth signing secret used to
// mint tokens on approve. The secret is the SAME one
// manager.WithScopedAuthSecret distributes to plugins; saas-
// starter mints with it server-side so plugins (which already
// hold the secret) can verify without an extra hop.
type DelegationServer struct {
	gen.UnimplementedDelegationServiceServer

	// SigningSecret is the HMAC secret for v1-hmac scoped-auth
	// tokens. Operators may also configure an ed25519 private
	// key (see SigningEd25519Key) to mint v2 tokens; if both
	// are set, v2 wins. If neither is set, DecideDelegation
	// returns an error on approve (we'd otherwise approve the
	// row without producing a usable token).
	SigningSecret []byte

	// SigningEd25519Key is the ed25519 private key for v2 tokens.
	// Optional; if set, takes precedence over SigningSecret.
	SigningEd25519Key []byte

	// mintCache holds recently-minted encoded tokens for the
	// streaming RPC to pick up. Bounded by mintedTokenTTL +
	// per-write sweep; never grows unbounded.
	mintCacheMu sync.Mutex
	mintCache   map[string]mintedToken
}

// =====================================================================
// RequestDelegation
// =====================================================================

func (s *DelegationServer) RequestDelegation(ctx context.Context, req *gen.RequestDelegationRequest) (*gen.RequestDelegationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	// Authz: caller must be the actor or an org member.
	// requireOrgMember handles the bulk of the check; for
	// agents acting on behalf of users, the actor_principal_id
	// must match the calling principal — guarded below.
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	// Must be requesting on behalf of the calling principal
	// (or by an org admin acting on behalf — for now require
	// exact match to keep the security contract simple).
	if req.GetActorPrincipalId() != actorID {
		// Allow org admins to file requests on behalf of others
		// (typical: a team lead files for an out-of-office
		// member). Non-admins must request as themselves.
		if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
			return nil, status.Error(codes.PermissionDenied,
				"actor_principal_id must match caller (or caller must be org admin)")
		}
	}

	timeout := time.Duration(req.GetTimeoutSeconds()) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	in := &business.RequestDelegationInput{
		OrgID:            req.GetOrgId(),
		ActorPrincipalID: req.GetActorPrincipalId(),
		Action:           req.GetAction(),
		Resource:         req.GetResource(),
		ResourceID:       req.GetResourceId(),
		Justification:    req.GetJustification(),
		Context:          req.GetContext().AsMap(),
		RiskLevel:        business.RiskLevel(req.GetRiskLevel()),
		Timeout:          timeout,
		Grantor:          req.GetGrantor(),
		RequestHash:      req.GetRequestHash(),
	}

	id, err := service.RequestDelegation(ctx, in)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Re-read to get expires_at (server-set) and the post-insert
	// status — M8 auto-approve produces status=approved here.
	grant, err := service.GetDelegation(ctx, id, req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// M8 auto-approved? Mint the token + cache it now so a
	// follow-up WaitForDelegation (the SDK pattern is "request
	// then wait") finds the encoded token without a second
	// round-trip. Mirrors the mint path in DecideDelegation so
	// approve-by-pattern and approve-by-grantor produce
	// identically-shaped tokens.
	//
	// Failure modes are advisory, not fatal: we already
	// committed the approved row; a mint failure here means
	// WaitForDelegation will emit an empty Token (the same
	// failure mode as approve-by-grantor with a missing key).
	// The actor sees the failure as soon as it tries to use the
	// scoped-auth header.
	if grant.Status == business.GrantStatusApproved && grant.Kind == business.GrantKindOneShot {
		w := wool.Get(ctx).In("RequestDelegation.autoApproveMint",
			wool.Field("grant_id", grant.ID))
		if actor, gerr := service.GetPrincipal(ctx, grant.ActorPrincipalID); gerr == nil {
			if token, sa, merr := s.mintApprovedToken(actor, grant); merr == nil {
				if serr := service.SetMintedToken(ctx, grant.ID, grant.OrgID, sa.ID); serr != nil {
					w.Warn("set minted_token_id failed for auto-approved grant",
						wool.ErrField(serr))
				}
				s.cacheMintedToken(grant.ID, mintedToken{Token: token, TokenID: sa.ID})
				grant.MintedTokenID = sa.ID
			} else {
				w.Warn("mint failed for auto-approved grant; Wait will see empty Token",
					wool.ErrField(merr))
			}
		} else {
			w.Warn("cannot load actor principal for auto-approved grant",
				wool.ErrField(gerr))
		}
	}

	return &gen.RequestDelegationResponse{
		Id:        id,
		Status:    string(grant.Status),
		ExpiresAt: timestamppb.New(grant.ExpiresAt),
	}, nil
}

// =====================================================================
// WaitForDelegation (server-streaming)
// =====================================================================

// WaitForDelegation streams the terminal decision for a grant.
// The agent SDK reads exactly one event and disconnects.
//
// Token-on-approve: the storage layer's Subscribe returns events
// where Token is empty (the row only stores the token_id, not
// the encoded token). This handler holds an in-memory map of
// recently-minted tokens (populated by DecideDelegation below)
// keyed by grant id; on approve, we look up + emit.
//
// Lifetime of the in-memory map: the streaming RPC and the
// DecideDelegation RPC share the same DelegationServer. A
// minted token lives in the map for ~30s (well past the typical
// streaming wait); aggressive eviction prevents memory growth
// in long-running servers.
func (s *DelegationServer) WaitForDelegation(req *gen.WaitForDelegationRequest, stream grpc.ServerStreamingServer[gen.DelegationEvent]) error {
	ctx := stream.Context()
	if err := Validate(req); err != nil {
		return err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return err
	}
	if err := requireOrgMember(ctx, actorID, req.GetOrgId()); err != nil {
		return err
	}

	w := wool.Get(ctx).In("WaitForDelegation",
		wool.Field("grant_id", req.GetId()))

	events, err := service.WaitForDelegation(ctx, req.GetId(), req.GetOrgId())
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	// Pull events; emit each. The channel closes on terminal
	// status or context cancellation.
	for ev := range events {
		grpcEvent := &gen.DelegationEvent{
			Id:                 ev.GrantID,
			Status:             string(ev.Status),
			GrantorPrincipalId: ev.GrantorID,
			Reason:             ev.Reason,
		}
		if ev.DecidedAt != nil {
			grpcEvent.DecidedAt = timestamppb.New(*ev.DecidedAt)
		}
		// On approve: look up the freshly-minted token from
		// our in-memory map and attach. Empty Token field =
		// the agent will NOT receive a usable token — caller
		// should treat as a server-side mint failure.
		if ev.Status == business.GrantStatusApproved {
			if minted := s.lookupMintedToken(ev.GrantID); minted != nil {
				grpcEvent.ScopedAuthToken = minted.Token
				grpcEvent.MintedTokenId = minted.TokenID
			} else {
				w.Warn("approved grant but no minted token in cache; agent will see empty token",
					wool.Field("grant_id", ev.GrantID))
			}
		}
		if err := stream.Send(grpcEvent); err != nil {
			return err
		}
	}
	return nil
}

// =====================================================================
// DecideDelegation
// =====================================================================

func (s *DelegationServer) DecideDelegation(ctx context.Context, req *gen.DecideDelegationRequest) (*gen.DelegationGrant, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Authz: caller must be a grantor (org admin) for this
	// org. Production may extend with role-based "can-approve-
	// for-this-action" checks; for now, org admins approve.
	if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}

	w := wool.Get(ctx).In("DecideDelegation",
		wool.Field("grant_id", req.GetId()),
		wool.Field("decision", req.GetDecision()))

	decision := business.DelegationGrantStatus(req.GetDecision())
	grant, err := service.DecideDelegation(ctx, req.GetId(), req.GetOrgId(), actorID, decision, req.GetReason())
	if err != nil {
		return nil, mapDelegationError(err)
	}

	// On approve: mint the scoped-auth token and persist its id.
	// Mint AFTER the atomic Decide so the row is locked into
	// approved state before we produce a token.
	if decision == business.GrantStatusApproved {
		actor, err := service.GetPrincipal(ctx, grant.ActorPrincipalID)
		if err != nil {
			// Critical: we approved but can't load the actor to
			// mint. Audit-log loud; the row is approved but the
			// minted_token_id stays empty so WaitForDelegation
			// emits an empty Token. Operators see this as a
			// "approved but mint failed" event.
			w.Warn("approved grant but cannot load actor principal; minted_token_id stays empty",
				wool.Field("error", err.Error()))
			return delegationGrantToProto(grant), nil
		}
		token, sa, err := s.mintApprovedToken(actor, grant)
		if err != nil {
			w.Warn("approved grant but mint failed; minted_token_id stays empty",
				wool.Field("error", err.Error()))
			return delegationGrantToProto(grant), nil
		}
		// Persist the token id (not the encoded token; we
		// don't want secrets in the database).
		if err := service.SetMintedToken(ctx, grant.ID, grant.OrgID, sa.ID); err != nil {
			w.Warn("set minted_token_id failed; streaming client may see empty Token",
				wool.Field("error", err.Error()))
		}
		// Cache the encoded token in the in-memory map so
		// WaitForDelegation can retrieve it. The map's
		// size-bounded by recently-decided grants.
		s.cacheMintedToken(grant.ID, mintedToken{Token: token, TokenID: sa.ID})
		grant.MintedTokenID = sa.ID
	}

	return delegationGrantToProto(grant), nil
}

// =====================================================================
// ListPendingDelegations
// =====================================================================

func (s *DelegationServer) ListPendingDelegations(ctx context.Context, req *gen.ListPendingDelegationsRequest) (*gen.ListPendingDelegationsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}

	grants, nextToken, err := service.ListPendingDelegations(ctx, req.GetOrgId(), req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	out := make([]*gen.DelegationGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, delegationGrantToProto(g))
	}
	return &gen.ListPendingDelegationsResponse{
		Grants:        out,
		NextPageToken: nextToken,
	}, nil
}

// =====================================================================
// Token mint
// =====================================================================

// mintApprovedToken mints a scoped-auth token for the approved
// grant. v2 (ed25519) when SigningEd25519Key is set, else v1
// (HMAC). The token bakes a `via_approval` caveat with the
// grant's metadata for audit correlation.
//
// **Why server-side mint.** The plugin already shares the
// signing key with the host that spawned it (manager.WithScoped
// AuthSecret). Saas-starter has the same key; minting here lets
// the streaming RPC return a ready-to-use token without a
// second round-trip through the host.
func (s *DelegationServer) mintApprovedToken(actor *business.Principal, grant *business.DelegationGrant) (string, *policy.ScopedAuthorization, error) {
	if len(s.SigningSecret) == 0 && len(s.SigningEd25519Key) == 0 {
		return "", nil, errors.New("DelegationServer: no signing key configured (set SigningSecret or SigningEd25519Key)")
	}

	// Translate business.Principal → policy.Principal.
	corePrincipal := &policy.Principal{
		ID:          actor.ID,
		Kind:        actor.Kind,
		OrgID:       actor.OrgID,
		AgentID:     actor.AgentIdentifier,
		DisplayName: actor.DisplayName,
	}

	// via_approval caveat carries the grant's audit metadata.
	// Plugin-side verifier (registered via auditCaveatVerifierFactory)
	// accepts any non-nil value — the signature already proved
	// authenticity.
	approvedAt := time.Now().Unix()
	if grant.DecidedAt != nil {
		approvedAt = grant.DecidedAt.Unix()
	}
	caveats := map[string]any{
		"via_approval": map[string]any{
			"grant_id":    grant.ID,
			"grantor_id":  grant.GrantorPrincipalID,
			"approved_at": approvedAt,
		},
	}

	mintInput := policy.MintInput{
		Principal: corePrincipal,
		Action:    grant.Action,
		Resource:  grant.Resource,
		// Audience left empty — the original tool that needed
		// the elevated authority is the audience. Plugin Guard
		// matches against its own audience; the gateway/agent
		// SDK that retrieves the token hands it to the right
		// plugin.
		AudienceID: "",
		// Short TTL — tokens are bearer credentials; the
		// shorter the window, the smaller the leak blast
		// radius. 5 minutes is the M7 default.
		TTL:     5 * time.Minute,
		MaxUses: 1,
		Caveats: caveats,
	}

	if len(s.SigningEd25519Key) == 64 {
		return policy.MintEd25519(mintInput, s.SigningEd25519Key)
	}
	if len(s.SigningSecret) >= 32 {
		return policy.Mint(mintInput, s.SigningSecret)
	}
	return "", nil, errors.New("DelegationServer: signing key configured but invalid length")
}

// =====================================================================
// In-memory minted-token cache
// =====================================================================

// mintedToken is the cached entry: encoded token + its id.
// Tokens are bearer credentials so we don't write the encoded
// form to the database; the cache lives only in this server's
// memory and times out aggressively.
type mintedToken struct {
	Token    string
	TokenID  string
	cachedAt time.Time
}

// mintedTokenTTL is how long a minted token stays in the cache.
// Long enough for the streaming RPC to pick it up after Decide
// fires NOTIFY (typically <1s), short enough that idle servers
// don't accumulate token strings.
const mintedTokenTTL = 60 * time.Second

func (s *DelegationServer) cacheMintedToken(grantID string, mt mintedToken) {
	s.mintCacheMu.Lock()
	defer s.mintCacheMu.Unlock()
	if s.mintCache == nil {
		s.mintCache = map[string]mintedToken{}
	}
	mt.cachedAt = time.Now()
	s.mintCache[grantID] = mt
	// Best-effort sweep on every write. O(n) where n is bounded
	// by the rate of approvals, so cheap.
	cutoff := time.Now().Add(-mintedTokenTTL)
	for id, entry := range s.mintCache {
		if entry.cachedAt.Before(cutoff) {
			delete(s.mintCache, id)
		}
	}
}

func (s *DelegationServer) lookupMintedToken(grantID string) *mintedToken {
	s.mintCacheMu.Lock()
	defer s.mintCacheMu.Unlock()
	mt, ok := s.mintCache[grantID]
	if !ok {
		return nil
	}
	if time.Since(mt.cachedAt) > mintedTokenTTL {
		delete(s.mintCache, grantID)
		return nil
	}
	return &mt
}

// =====================================================================
// Helpers
// =====================================================================

func delegationGrantToProto(g *business.DelegationGrant) *gen.DelegationGrant {
	if g == nil {
		return nil
	}
	out := &gen.DelegationGrant{
		Id:                 g.ID,
		OrgId:              g.OrgID,
		ActorPrincipalId:   g.ActorPrincipalID,
		GrantorPrincipalId: g.GrantorPrincipalID,
		Action:             g.Action,
		Resource:           g.Resource,
		ResourceId:         g.ResourceID,
		Justification:      g.Justification,
		Status:             string(g.Status),
		RiskLevel:          string(g.RiskLevel),
		Kind:               string(g.Kind),
		CreatedAt:          timestamppb.New(g.CreatedAt),
		ExpiresAt:          timestamppb.New(g.ExpiresAt),
		DecisionReason:     g.DecisionReason,
		MintedTokenId:      g.MintedTokenID,
	}
	if g.DecidedAt != nil {
		out.DecidedAt = timestamppb.New(*g.DecidedAt)
	}
	return out
}

func mapDelegationError(err error) error {
	if err == nil {
		return nil
	}
	var se *business.StoreError
	if errors.As(err, &se) {
		switch se.StoreErrorType {
		case business.ErrTypeNotFound:
			return status.Error(codes.NotFound, err.Error())
		case business.ErrTypeConflict:
			return status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	// Validation errors from the business layer are wrapped via
	// fmt.Errorf; surface as InvalidArgument when the message
	// hints at it.
	if errors.Is(err, errors.New("validate")) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// Compile-time guard: DelegationServer satisfies the gen
// interface.
var _ gen.DelegationServiceServer = (*DelegationServer)(nil)

// =====================================================================
// Context import (silence unused) — used in WaitForDelegation handler
// =====================================================================

var _ = context.Background
