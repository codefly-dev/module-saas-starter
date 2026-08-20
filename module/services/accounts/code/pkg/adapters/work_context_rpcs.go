package adapters

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type WorkContextAuthorityConfiguration struct {
	Issuer     string
	KeyID      string
	PrivateKey []byte
	Authority  business.WorkContextAuthorityStore
}

// WorkContextAuthorityServer is the permissions-plugin-owned issuer. It never
// owns consumer Task/Session rows: it binds current Accounts identity and RBAC
// facts into a short-lived Codefly capability.
type WorkContextAuthorityServer struct {
	gen.UnimplementedWorkContextServiceServer

	issuer       string
	signer       *codefly.WorkContextSigner
	verifier     *codefly.WorkContextVerifier
	authority    business.WorkContextAuthorityStore
	consumer     business.WorkContextConsumerAuthorityStore
	journal      business.ActorChainJournal
	configureErr error
}

func (s *WorkContextAuthorityServer) Configure(config WorkContextAuthorityConfiguration) {
	s.issuer = strings.TrimSpace(config.Issuer)
	s.signer = nil
	s.verifier = nil
	s.authority = nil
	s.consumer = nil
	s.journal = nil
	s.configureErr = nil

	if s.issuer == "" || strings.TrimSpace(config.KeyID) == "" {
		s.configureErr = errors.New("work-context issuer and key ID are required")
		return
	}
	if len(config.PrivateKey) != ed25519.PrivateKeySize {
		s.configureErr = fmt.Errorf("work-context Ed25519 private key must be %d bytes", ed25519.PrivateKeySize)
		return
	}
	if config.Authority == nil {
		s.configureErr = errors.New("accounts store does not implement WorkContextAuthorityStore")
		return
	}
	privateKey := append(ed25519.PrivateKey(nil), config.PrivateKey...)
	signer, err := codefly.NewWorkContextSigner(codefly.WorkContextSignerOptions{
		Issuer:     s.issuer,
		KeyID:      config.KeyID,
		PrivateKey: privateKey,
	})
	if err != nil {
		s.configureErr = err
		return
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		s.configureErr = errors.New("work-context signing key has no Ed25519 public key")
		return
	}
	verifier, err := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{
		PublicKeys: map[string]ed25519.PublicKey{config.KeyID: publicKey},
	})
	if err != nil {
		s.configureErr = err
		return
	}
	s.signer = signer
	s.verifier = verifier
	s.authority = config.Authority
	s.consumer, _ = config.Authority.(business.WorkContextConsumerAuthorityStore)
	s.journal, _ = config.Authority.(business.ActorChainJournal)
}

func (s *WorkContextAuthorityServer) CheckAuthorizationRevision(
	ctx context.Context,
	req *gen.CheckAuthorizationRevisionRequest,
) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.consumer == nil {
		return nil, status.Error(codes.Unavailable, "Work Context consumer authority is unavailable")
	}
	if len(req.GetSubjects()) == 0 ||
		req.GetSubjects()[0].GetPrincipalId() != req.GetOwnerPrincipalId() {
		return nil, status.Error(codes.InvalidArgument, "owner must be the first revision subject")
	}
	seen := make(map[string]struct{}, len(req.GetSubjects()))
	subjects := make([]business.WorkContextRevisionSubject, 0, len(req.GetSubjects()))
	for _, subject := range req.GetSubjects() {
		if subject == nil {
			return nil, status.Error(codes.InvalidArgument, "revision subject must not be null")
		}
		principalID := subject.GetPrincipalId()
		if _, duplicate := seen[principalID]; duplicate {
			return nil, status.Error(codes.InvalidArgument, "revision subjects must be unique")
		}
		seen[principalID] = struct{}{}
		permissions, _, err := workContextScopes(subject.GetScopes())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		subjects = append(subjects, business.WorkContextRevisionSubject{
			PrincipalID: principalID,
			Permissions: permissions,
		})
	}
	ctx = accountsauth.WithVerifiedDatabaseIdentity(
		ctx,
		req.GetOwnerPrincipalId(),
		req.GetOrgId(),
	)
	err := s.consumer.CheckWorkContextAuthorizationRevision(
		ctx,
		req.GetOrgId(),
		req.GetOwnerPrincipalId(),
		req.GetAuthorizationRevision(),
		subjects,
	)
	if err != nil {
		return nil, mapConsumerAuthorityError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *WorkContextAuthorityServer) AuthorizeEvidenceRead(
	ctx context.Context,
	req *gen.AuthorizeEvidenceReadRequest,
) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.consumer == nil {
		return nil, status.Error(codes.Unavailable, "Evidence read authority is unavailable")
	}
	ctx = accountsauth.WithVerifiedDatabaseIdentity(
		ctx,
		req.GetCallerPrincipalId(),
		req.GetOrgId(),
	)
	err := s.consumer.AuthorizeEvidenceRead(
		ctx,
		req.GetOrgId(),
		req.GetCallerPrincipalId(),
		req.GetOwnerPrincipalId(),
		req.GetTaskId(),
		req.GetSessionId(),
	)
	if err != nil {
		return nil, mapConsumerAuthorityError(err)
	}
	return &emptypb.Empty{}, nil
}

func mapConsumerAuthorityError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "consumer authority check canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "consumer authority check timed out")
	case errors.Is(err, business.ErrWorkContextAuthorizationStale):
		return status.Error(codes.FailedPrecondition, business.ErrWorkContextAuthorizationStale.Error())
	case errors.Is(err, business.ErrEvidenceReadDenied):
		return status.Error(codes.PermissionDenied, business.ErrEvidenceReadDenied.Error())
	default:
		return status.Error(codes.Internal, "cannot resolve current consumer authority")
	}
}

func (s *WorkContextAuthorityServer) StartTask(
	ctx context.Context,
	req *gen.StartTaskWorkContextRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	permissions, scopes, err := workContextScopes(req.GetAuthorityScopes())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	facts, err := s.resolveAuthority(
		ctx, req.GetOrgId(), ownerID, req.GetActorPrincipalId(), permissions,
	)
	if err != nil {
		return nil, err
	}

	var actors []*basev0.WorkActorV1
	if facts.Actor != nil {
		actors = []*basev0.WorkActorV1{{
			PrincipalId:   facts.Actor.ID,
			PrincipalKind: facts.Actor.Kind,
			// The per-hop delegation id IS the durable journal row id: the
			// ephemeral token and the persisted record share one identifier.
			DelegationId:  uuid.NewString(),
			GrantedScopes: cloneWorkScopes(scopes),
		}}
	}
	token, signed, err := s.signer.StartTask(codefly.StartTaskInput{
		Audience:              req.GetAudience(),
		TenantID:              req.GetOrgId(),
		OwnerPrincipalID:      ownerID,
		TaskID:                req.GetTaskId(),
		SessionID:             req.GetSessionId(),
		AuthorizationRevision: facts.EffectiveRevision(),
		ReplayPolicy:          workContextReplayPolicy(req.GetReplayPolicy()),
		AuthorityScopes:       scopes,
		ActorChain:            actors,
		AttributionTeamIDs:    facts.AttributionTeamIDs,
		WorkspaceID:           req.GetWorkspaceId(),
		ProjectID:             req.GetProjectId(),
		TTL:                   workContextTTL(req.GetTtlSeconds()),
	})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	if err := s.journalActorHop(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

func (s *WorkContextAuthorityServer) StartRootSession(
	ctx context.Context,
	req *gen.StartRootSessionWorkContextRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	parentToken, parent, err := s.verifyParent(
		ctx, req.GetOrgId(), ownerID, req.GetParentWorkContextToken(),
	)
	if err != nil {
		return nil, err
	}
	if req.GetSessionId() == parent.GetSessionId() {
		return nil, status.Error(codes.InvalidArgument, "new session_id must differ from parent session")
	}
	token, signed, err := s.signer.StartSession(parentToken, codefly.StartRootSessionInput{
		SessionID:    req.GetSessionId(),
		Audience:     req.GetAudience(),
		ReplayPolicy: workContextReplayPolicy(req.GetReplayPolicy()),
		TTL:          workContextTTL(req.GetTtlSeconds()),
	})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	return issuedWorkContext(token, signed), nil
}

func (s *WorkContextAuthorityServer) ExchangeAudience(
	ctx context.Context,
	req *gen.ExchangeWorkContextAudienceRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	parentToken, parent, err := s.verifyParent(
		ctx, req.GetOrgId(), ownerID, req.GetParentWorkContextToken(),
	)
	if err != nil {
		return nil, err
	}
	if req.GetAudience() == parent.GetAudience() {
		return nil, status.Error(codes.InvalidArgument, "new audience must differ from parent audience")
	}
	_, scopes, err := workContextScopes(req.GetAttenuatedScopes())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	token, signed, err := s.signer.ExchangeWorkContextAudience(
		parentToken,
		codefly.ExchangeWorkContextAudienceInput{
			Audience:         req.GetAudience(),
			ReplayPolicy:     workContextReplayPolicy(req.GetReplayPolicy()),
			TTL:              workContextTTL(req.GetTtlSeconds()),
			AttenuatedScopes: scopes,
		},
	)
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	return issuedWorkContext(token, signed), nil
}

func (s *WorkContextAuthorityServer) StartChildSession(
	ctx context.Context,
	req *gen.StartChildSessionWorkContextRequest,
) (*gen.IssuedWorkContext, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	ownerID, err := s.authorizeOwner(ctx, req.GetOrgId())
	if err != nil {
		return nil, err
	}
	parentToken, parent, err := s.verifyParent(
		ctx, req.GetOrgId(), ownerID, req.GetParentWorkContextToken(),
	)
	if err != nil {
		return nil, err
	}
	if req.GetSessionId() == parent.GetSessionId() {
		return nil, status.Error(codes.InvalidArgument, "child session_id must differ from parent session")
	}
	permissions, scopes, err := workContextScopes(req.GetGrantedScopes())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	facts, err := s.resolveAuthority(
		ctx, req.GetOrgId(), ownerID, req.GetActorPrincipalId(), permissions,
	)
	if err != nil {
		return nil, err
	}
	if facts.EffectiveRevision() != parent.GetAuthorizationRevision() {
		return nil, status.Error(
			codes.FailedPrecondition,
			"parent Work Context authorization revision is stale",
		)
	}
	token, signed, err := s.signer.StartChildSession(parentToken, codefly.StartChildSessionInput{
		SessionID: req.GetSessionId(),
		Audience:  req.GetAudience(),
		Actor: &basev0.WorkActorV1{
			PrincipalId:   facts.Actor.ID,
			PrincipalKind: facts.Actor.Kind,
			DelegationId:  uuid.NewString(),
			GrantedScopes: scopes,
		},
		ReplayPolicy: workContextReplayPolicy(req.GetReplayPolicy()),
		TTL:          workContextTTL(req.GetTtlSeconds()),
	})
	if err != nil {
		return nil, mapWorkContextError(err)
	}
	if err := s.journalActorHop(ctx, signed); err != nil {
		return nil, err
	}
	return issuedWorkContext(token, signed), nil
}

// journalActorHop durably records the newest on-behalf-of hop of a freshly
// signed Work Context, content-addressed and chained to the hop it narrows from.
// The hop id equals the token's per-hop delegation_id, so the durable record and
// the ephemeral token share one identifier. Durability is a guarantee, not
// best-effort: a failed append fails issuance rather than minting an
// unaccountable token.
func (s *WorkContextAuthorityServer) journalActorHop(
	ctx context.Context,
	signed *basev0.WorkContextV1,
) error {
	if s.journal == nil {
		return nil
	}
	chain := signed.GetActorChain()
	if len(chain) == 0 {
		return nil
	}
	index := len(chain) - 1
	actor := chain[index]
	parentDelegationID := ""
	if index > 0 {
		parentDelegationID = chain[index-1].GetDelegationId()
	}
	_, err := s.journal.AppendActorChainHop(ctx, business.ActorChainHopInput{
		ID:                    actor.GetDelegationId(),
		OrgID:                 signed.GetTenantId(),
		TaskID:                signed.GetTaskId(),
		SessionID:             signed.GetSessionId(),
		OwnerPrincipalID:      signed.GetOwnerPrincipalId(),
		ActorPrincipalID:      actor.GetPrincipalId(),
		ActorKind:             actor.GetPrincipalKind(),
		ParentDelegationID:    parentDelegationID,
		GrantedScopes:         actorChainScopes(actor.GetGrantedScopes()),
		AuthorizationRevision: signed.GetAuthorizationRevision(),
		HopIndex:              index,
	})
	if err != nil {
		return status.Error(codes.Internal, "cannot persist actor chain hop")
	}
	return nil
}

// requireChainNotRevoked fails closed if any hop in the parent chain, or any
// ancestor those hops chain through, has been revoked. This is the per-hop
// revocation layer that stops a revoked branch from deriving new tokens; the
// revocation also bumps the owner/org authorization revision (see migration 99)
// so already-issued tokens on that branch are rejected on the action path too.
// The whole chain is checked in one query. A hop with no journal record is
// treated as live: journalActorHop fails issuance on a failed append, so no
// token issued after durability shipped can lack its hop; the only unjournaled
// hops predate the journal and cannot have been revoked (revocation needs a row).
func (s *WorkContextAuthorityServer) requireChainNotRevoked(
	ctx context.Context,
	orgID string,
	actors []*basev0.WorkActorV1,
) error {
	if s.journal == nil {
		return nil
	}
	delegationIDs := make([]string, 0, len(actors))
	for _, actor := range actors {
		if id := actor.GetDelegationId(); id != "" {
			delegationIDs = append(delegationIDs, id)
		}
	}
	if len(delegationIDs) == 0 {
		return nil
	}
	revoked, err := s.journal.AnyActorChainHopRevoked(ctx, orgID, delegationIDs)
	if err != nil {
		return status.Error(codes.Internal, "cannot check actor chain revocation")
	}
	if revoked {
		return status.Error(codes.PermissionDenied, "actor chain hop has been revoked")
	}
	return nil
}

func actorChainScopes(scopes []*basev0.WorkScopeV1) []business.ActorChainScope {
	out := make([]business.ActorChainScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, business.ActorChainScope{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIDs:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return out
}

func (s *WorkContextAuthorityServer) authorizeOwner(ctx context.Context, orgID string) (string, error) {
	if s == nil || s.configureErr != nil || s.signer == nil || s.verifier == nil || s.authority == nil {
		return "", status.Error(codes.FailedPrecondition, "Work Context authority is not configured")
	}
	ownerID, err := requireAuth(ctx)
	if err != nil {
		return "", err
	}
	if err := requireOrgMember(ctx, ownerID, orgID); err != nil {
		return "", err
	}
	return ownerID, nil
}

func (s *WorkContextAuthorityServer) verifyParent(
	ctx context.Context,
	orgID string,
	ownerID string,
	encoded string,
) (codefly.WorkContextToken, *basev0.WorkContextV1, error) {
	token, err := codefly.ParseWorkContextToken(encoded)
	if err != nil {
		return codefly.WorkContextToken{}, nil, status.Error(codes.InvalidArgument, "invalid parent Work Context")
	}
	parent, err := s.verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:           s.issuer,
		TenantID:         orgID,
		OwnerPrincipalID: ownerID,
	})
	if err != nil {
		return codefly.WorkContextToken{}, nil, status.Error(codes.PermissionDenied, "parent Work Context is not valid for caller")
	}

	if len(parent.GetActorChain()) == 0 {
		permissions := permissionsFromCoreScopes(parent.GetAuthorityScopes())
		facts, resolveErr := s.resolveAuthority(ctx, orgID, ownerID, "", permissions)
		if resolveErr != nil {
			return codefly.WorkContextToken{}, nil, resolveErr
		}
		if facts.EffectiveRevision() != parent.GetAuthorizationRevision() {
			return codefly.WorkContextToken{}, nil, status.Error(
				codes.FailedPrecondition,
				"parent Work Context authorization revision is stale",
			)
		}
		return token, parent, nil
	}

	if err := s.requireChainNotRevoked(ctx, orgID, parent.GetActorChain()); err != nil {
		return codefly.WorkContextToken{}, nil, err
	}

	for _, actor := range parent.GetActorChain() {
		permissions := permissionsFromCoreScopes(actor.GetGrantedScopes())
		facts, resolveErr := s.resolveAuthority(
			ctx, orgID, ownerID, actor.GetPrincipalId(), permissions,
		)
		if resolveErr != nil {
			return codefly.WorkContextToken{}, nil, resolveErr
		}
		if facts.EffectiveRevision() != parent.GetAuthorizationRevision() {
			return codefly.WorkContextToken{}, nil, status.Error(
				codes.FailedPrecondition,
				"parent Work Context authorization revision is stale",
			)
		}
	}
	return token, parent, nil
}

func (s *WorkContextAuthorityServer) resolveAuthority(
	ctx context.Context,
	orgID string,
	ownerID string,
	actorID string,
	permissions []business.WorkContextPermission,
) (*business.WorkContextAuthorityFacts, error) {
	facts, err := s.authority.ResolveWorkContextAuthority(
		ctx, orgID, ownerID, actorID, permissions,
	)
	if err == nil {
		if actorID != "" && facts.Actor == nil {
			return nil, status.Error(codes.Internal, "authority store omitted requested actor")
		}
		return facts, nil
	}
	var storeErr *business.StoreError
	if errors.As(err, &storeErr) {
		switch storeErr.StoreErrorType {
		case business.ErrTypeNotFound:
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		case business.ErrTypePermission:
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
	}
	return nil, status.Error(codes.Internal, "cannot resolve current Work Context authority")
}

func workContextScopes(
	input []*gen.WorkContextScope,
) ([]business.WorkContextPermission, []*basev0.WorkScopeV1, error) {
	permissions := make([]business.WorkContextPermission, 0)
	scopes := make([]*basev0.WorkScopeV1, 0, len(input))
	seenScopes := make(map[string]struct{}, len(input))
	for _, scope := range input {
		if scope == nil {
			return nil, nil, errors.New("authority scope must not be null")
		}
		resourceKind := strings.TrimSpace(scope.GetResourceKind())
		if resourceKind != scope.GetResourceKind() {
			return nil, nil, errors.New("resource_kind must not have surrounding whitespace")
		}
		coreScope := &basev0.WorkScopeV1{
			ResourceKind: resourceKind,
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIds:  append([]string(nil), scope.GetResourceIds()...),
		}
		scopeKey := resourceKind + "\x00" + strings.Join(coreScope.Actions, "\x00") +
			"\x01" + strings.Join(coreScope.ResourceIds, "\x00")
		if _, exists := seenScopes[scopeKey]; exists {
			return nil, nil, errors.New("duplicate authority scope")
		}
		seenScopes[scopeKey] = struct{}{}

		for _, action := range coreScope.Actions {
			if action != strings.TrimSpace(action) {
				return nil, nil, errors.New("scope action must not have surrounding whitespace")
			}
			if len(coreScope.ResourceIds) == 0 {
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: resourceKind,
					Action:       action,
				})
				continue
			}
			for _, resourceID := range coreScope.ResourceIds {
				if resourceID != strings.TrimSpace(resourceID) {
					return nil, nil, errors.New("resource_id must not have surrounding whitespace")
				}
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: resourceKind,
					Action:       action,
					ResourceID:   resourceID,
				})
			}
		}
		scopes = append(scopes, coreScope)
	}
	return permissions, scopes, nil
}

func permissionsFromCoreScopes(scopes []*basev0.WorkScopeV1) []business.WorkContextPermission {
	permissions := make([]business.WorkContextPermission, 0)
	for _, scope := range scopes {
		for _, action := range scope.GetActions() {
			if len(scope.GetResourceIds()) == 0 {
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: scope.GetResourceKind(),
					Action:       action,
				})
				continue
			}
			for _, resourceID := range scope.GetResourceIds() {
				permissions = append(permissions, business.WorkContextPermission{
					ResourceKind: scope.GetResourceKind(),
					Action:       action,
					ResourceID:   resourceID,
				})
			}
		}
	}
	return permissions
}

func cloneWorkScopes(scopes []*basev0.WorkScopeV1) []*basev0.WorkScopeV1 {
	out := make([]*basev0.WorkScopeV1, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, &basev0.WorkScopeV1{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIds:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return out
}

func workContextReplayPolicy(value gen.WorkContextReplayPolicy) string {
	if value == gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_SINGLE_USE {
		return codefly.WorkContextReplaySingleUse
	}
	return codefly.WorkContextReplayIdempotent
}

func workContextTTL(seconds int32) time.Duration {
	if seconds == 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func issuedWorkContext(
	token codefly.WorkContextToken,
	signed *basev0.WorkContextV1,
) *gen.IssuedWorkContext {
	currentActor := signed.GetOwnerPrincipalId()
	if actors := signed.GetActorChain(); len(actors) > 0 {
		currentActor = actors[len(actors)-1].GetPrincipalId()
	}
	response := &gen.IssuedWorkContext{
		Token:                   token.Encoded(),
		OrgId:                   signed.GetTenantId(),
		OwnerPrincipalId:        signed.GetOwnerPrincipalId(),
		TaskId:                  signed.GetTaskId(),
		SessionId:               signed.GetSessionId(),
		CurrentActorPrincipalId: currentActor,
		AuthorizationRevision:   signed.GetAuthorizationRevision(),
		ExpiresAt:               timestamppb.New(time.Unix(signed.GetExpiresAtUnix(), 0).UTC()),
	}
	if signed.ParentSessionId != nil {
		parent := signed.GetParentSessionId()
		response.ParentSessionId = &parent
	}
	if signed.WorkspaceId != nil {
		workspaceID := signed.GetWorkspaceId()
		response.WorkspaceId = &workspaceID
	}
	if signed.ProjectId != nil {
		projectID := signed.GetProjectId()
		response.ProjectId = &projectID
	}
	return response
}

func mapWorkContextError(err error) error {
	if errors.Is(err, codefly.ErrWorkContextInvalid) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Error(codes.Internal, "cannot issue Work Context")
}

var _ gen.WorkContextServiceServer = (*WorkContextAuthorityServer)(nil)
