package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type workContextAuthorityFake struct {
	facts *business.WorkContextAuthorityFacts
	err   error
}

func (f *workContextAuthorityFake) ResolveWorkContextAuthority(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []business.WorkContextPermission,
) (*business.WorkContextAuthorityFacts, error) {
	return f.facts, f.err
}

type workContextConsumerAuthorityFake struct {
	workContextAuthorityFake
	revisionOrg      string
	revisionOwner    string
	revisionExpected uint64
	revisionSubjects []business.WorkContextRevisionSubject
	revisionErr      error
	readOrg          string
	readCaller       string
	readOwner        string
	readTask         string
	readSession      string
	readErr          error
	verifiedTenant   string
	verifiedUser     string
}

func (fake *workContextConsumerAuthorityFake) CheckWorkContextAuthorizationRevision(
	ctx context.Context,
	orgID string,
	ownerPrincipalID string,
	expectedRevision uint64,
	subjects []business.WorkContextRevisionSubject,
) error {
	fake.revisionOrg = orgID
	fake.revisionOwner = ownerPrincipalID
	fake.revisionExpected = expectedRevision
	fake.revisionSubjects = subjects
	fake.verifiedTenant, fake.verifiedUser, _ = accountsauth.VerifiedDatabaseIdentity(ctx)
	return fake.revisionErr
}

func (fake *workContextConsumerAuthorityFake) AuthorizeEvidenceRead(
	ctx context.Context,
	orgID string,
	callerPrincipalID string,
	ownerPrincipalID string,
	taskID string,
	sessionID string,
) error {
	fake.readOrg = orgID
	fake.readCaller = callerPrincipalID
	fake.readOwner = ownerPrincipalID
	fake.readTask = taskID
	fake.readSession = sessionID
	fake.verifiedTenant, fake.verifiedUser, _ = accountsauth.VerifiedDatabaseIdentity(ctx)
	return fake.readErr
}

type workContextReplayFake struct {
	workContextAuthorityFake
	consumedOrg     string
	consumedContext string
	consumedExpiry  time.Time
	consumeErr      error
}

func (f *workContextReplayFake) ConsumeSingleUseWorkContext(
	_ context.Context,
	orgID string,
	contextID string,
	expiresAt time.Time,
) error {
	f.consumedOrg = orgID
	f.consumedContext = contextID
	f.consumedExpiry = expiresAt
	return f.consumeErr
}

func (f *workContextReplayFake) PurgeExpiredWorkContextReplays(
	_ context.Context,
	_ time.Time,
) (int64, error) {
	return 0, nil
}

func TestWorkContextConsumeSingleUseEnforcesReplayFailClosed(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	replay := &workContextReplayFake{}
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  replay,
	})
	require.NoError(t, server.configureErr)

	orgID := "019fec91-2000-7000-8000-000000000001"
	expiresAt := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	request := &gen.ConsumeSingleUseWorkContextRequest{
		OrgId:     orgID,
		ContextId: "nonce-abc",
		ExpiresAt: timestamppb.New(expiresAt),
	}

	_, err = server.ConsumeSingleUse(context.Background(), request)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	SetInternalToken("consume-single-use-test-token")
	t.Cleanup(func() { SetInternalToken("") })
	internalContext := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-codefly-internal-token", "consume-single-use-test-token"),
	)

	_, err = server.ConsumeSingleUse(internalContext, request)
	require.NoError(t, err)
	require.Equal(t, orgID, replay.consumedOrg)
	require.Equal(t, "nonce-abc", replay.consumedContext)
	require.Equal(t, expiresAt, replay.consumedExpiry)

	replay.consumeErr = business.ErrWorkContextAlreadyConsumed
	_, err = server.ConsumeSingleUse(internalContext, request)
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	replay.consumeErr = errors.New("database unavailable")
	_, err = server.ConsumeSingleUse(internalContext, request)
	require.Equal(t, codes.Internal, status.Code(err))
}

func TestWorkContextConsumeSingleUseRejectsFarFutureExpiry(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	replay := &workContextReplayFake{}
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  replay,
	})
	require.NoError(t, server.configureErr)

	SetInternalToken("consume-single-use-test-token")
	t.Cleanup(func() { SetInternalToken("") })
	internalContext := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-codefly-internal-token", "consume-single-use-test-token"),
	)

	_, err = server.ConsumeSingleUse(internalContext, &gen.ConsumeSingleUseWorkContextRequest{
		OrgId:     "019fec91-2000-7000-8000-000000000001",
		ContextId: "nonce-abc",
		ExpiresAt: timestamppb.New(time.Now().Add(48 * time.Hour)),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Empty(t, replay.consumedContext, "a capability with an implausible expiry must never reach the store")
}

func TestWorkContextConsumeSingleUseUnavailableWithoutReplayStore(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// A store that resolves authority but does not back the replay seam leaves
	// single-use unavailable rather than silently unenforced.
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  &workContextAuthorityFake{},
	})
	require.NoError(t, server.configureErr)
	require.Nil(t, server.replay)

	SetInternalToken("consume-single-use-test-token")
	t.Cleanup(func() { SetInternalToken("") })
	internalContext := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-codefly-internal-token", "consume-single-use-test-token"),
	)
	_, err = server.ConsumeSingleUse(internalContext, &gen.ConsumeSingleUseWorkContextRequest{
		OrgId:     "019fec91-2000-7000-8000-000000000001",
		ContextId: "nonce-abc",
		ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestWorkContextScopesMapExactPermissionChecks(t *testing.T) {
	permissions, scopes, err := workContextScopes([]*gen.WorkContextScope{{
		ResourceKind: "repository",
		Actions:      []string{"read", "write"},
		ResourceIds:  []string{"repo:one", "repo:two"},
	}})
	require.NoError(t, err)
	require.Equal(t, []*basev0.WorkScopeV1{{
		ResourceKind: "repository",
		Actions:      []string{"read", "write"},
		ResourceIds:  []string{"repo:one", "repo:two"},
	}}, scopes)
	require.Equal(t, []business.WorkContextPermission{
		{ResourceKind: "repository", Action: "read", ResourceID: "repo:one"},
		{ResourceKind: "repository", Action: "read", ResourceID: "repo:two"},
		{ResourceKind: "repository", Action: "write", ResourceID: "repo:one"},
		{ResourceKind: "repository", Action: "write", ResourceID: "repo:two"},
	}, permissions)
}

func TestWorkContextParentVerificationRejectsStaleAuthorization(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authority := &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{
		OrganizationRevision: 12,
		PrincipalRevision:    13,
	}}
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  authority,
	})
	require.NoError(t, server.configureErr)

	token, _, err := server.signer.StartTask(codefly.StartTaskInput{
		Audience:              "consumer.test",
		TenantID:              "019f6bf7-5b4b-74e5-8c17-092259bb1661",
		OwnerPrincipalID:      "019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
		TaskID:                "019f6bf7-1111-7111-8111-111111111111",
		SessionID:             "019f6bf7-2222-7222-8222-222222222222",
		AuthorizationRevision: 12,
		ReplayPolicy:          codefly.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{{
			ResourceKind: "evidence",
			Actions:      []string{"append"},
		}},
	})
	require.NoError(t, err)

	_, _, _, err = server.verifyParent(
		context.Background(),
		"019f6bf7-5b4b-74e5-8c17-092259bb1661",
		"019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
		token.Encoded(),
	)
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestWorkContextAuthorityConfigurationFailsClosed(t *testing.T) {
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{})
	require.Error(t, server.configureErr)
	require.Nil(t, server.signer)
	require.Nil(t, server.verifier)
	require.Nil(t, server.authority)
}

func TestWorkContextConsumerAuthorityRequiresInternalCredentialAndProjectsExactFacts(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	fake := &workContextConsumerAuthorityFake{}
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  fake,
	})
	require.NoError(t, server.configureErr)

	orgID := "019fec91-1000-7000-8000-000000000001"
	ownerID := "019fec91-1000-7000-8000-000000000002"
	actorID := "019fec91-1000-7000-8000-000000000003"
	revisionRequest := &gen.CheckAuthorizationRevisionRequest{
		OrgId:                 orgID,
		OwnerPrincipalId:      ownerID,
		AuthorizationRevision: 29,
		Subjects: []*gen.WorkContextRevisionSubject{
			{
				PrincipalId: ownerID,
				Scopes: []*gen.WorkContextScope{{
					ResourceKind: "evidence",
					Actions:      []string{"append"},
					ResourceIds:  []string{"task:one"},
				}},
			},
			{PrincipalId: actorID},
		},
	}
	_, err = server.CheckAuthorizationRevision(context.Background(), revisionRequest)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	SetInternalToken("consumer-authority-test-token")
	t.Cleanup(func() { SetInternalToken("") })
	internalContext := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-codefly-internal-token", "consumer-authority-test-token"),
	)
	_, err = server.CheckAuthorizationRevision(internalContext, revisionRequest)
	require.NoError(t, err)
	require.Equal(t, orgID, fake.revisionOrg)
	require.Equal(t, ownerID, fake.revisionOwner)
	require.Equal(t, uint64(29), fake.revisionExpected)
	require.Len(t, fake.revisionSubjects, 2)
	require.Equal(t, "evidence", fake.revisionSubjects[0].Permissions[0].ResourceKind)
	require.Equal(t, orgID, fake.verifiedTenant)
	require.Equal(t, ownerID, fake.verifiedUser)

	fake.revisionErr = business.ErrWorkContextAuthorizationStale
	_, err = server.CheckAuthorizationRevision(internalContext, revisionRequest)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	fake.revisionErr = errors.New("database unavailable")
	_, err = server.CheckAuthorizationRevision(internalContext, revisionRequest)
	require.Equal(t, codes.Internal, status.Code(err))

	fake.revisionErr = nil
	callerID := "019fec91-1000-7000-8000-000000000004"
	taskID := "task:one"
	_, err = server.AuthorizeEvidenceRead(internalContext, &gen.AuthorizeEvidenceReadRequest{
		OrgId:             orgID,
		CallerPrincipalId: callerID,
		OwnerPrincipalId:  &ownerID,
		TaskId:            &taskID,
	})
	require.NoError(t, err)
	require.Equal(t, orgID, fake.readOrg)
	require.Equal(t, callerID, fake.readCaller)
	require.Equal(t, ownerID, fake.readOwner)
	require.Equal(t, "task:one", fake.readTask)
	require.Equal(t, callerID, fake.verifiedUser)

	fake.readErr = business.ErrEvidenceReadDenied
	_, err = server.AuthorizeEvidenceRead(internalContext, &gen.AuthorizeEvidenceReadRequest{
		OrgId:             orgID,
		CallerPrincipalId: callerID,
		OwnerPrincipalId:  &ownerID,
		TaskId:            &taskID,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

const (
	renewOrgID   = "019f6bf7-5b4b-74e5-8c17-092259bb1671"
	renewOwnerID = "019f6bf7-5b1c-730d-9687-fe6d4aff3201"
	renewActorID = "019f6bf7-5b1c-730d-9687-fe6d4aff3202"
	renewTaskID  = "019f6bf7-1111-7111-8111-111111111171"
	renewSession = "019f6bf7-2222-7222-8222-222222222271"
)

// renewMembershipStore is the minimal Store an actor-authorized renewal reads:
// requireOrgMember resolves the caller's membership and nothing else.
type renewMembershipStore struct {
	business.Store
}

func (renewMembershipStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (renewMembershipStore) GetPlatformRole(context.Context, string) (string, error) {
	return "", nil
}

func (renewMembershipStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	if orgID == renewOrgID && userID == renewActorID {
		return &gen.OrgMembership{UserId: userID, Role: gen.OrgRole_ORG_ROLE_MEMBER}, nil
	}
	return nil, nil
}

func newRenewTestServer(t *testing.T) *WorkContextAuthorityServer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority: &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{
			OrganizationRevision: 12,
			Actor:                &business.Principal{ID: renewActorID, Kind: "agent"},
		}},
	})
	require.NoError(t, server.configureErr)
	return server
}

// mintDelegatedContext issues a Work Context whose current actor is renewActorID,
// modelling a context an in-flight agent holds while acting for the owner.
func mintDelegatedContext(t *testing.T, server *WorkContextAuthorityServer) codefly.WorkContextToken {
	t.Helper()
	token, _, err := server.signer.StartTask(codefly.StartTaskInput{
		Audience:              "tool.test",
		TenantID:              renewOrgID,
		OwnerPrincipalID:      renewOwnerID,
		TaskID:                renewTaskID,
		SessionID:             renewSession,
		AuthorizationRevision: 12,
		ReplayPolicy:          codefly.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{{
			ResourceKind: "evidence",
			Actions:      []string{"append", "read"},
		}},
		ActorChain: []*basev0.WorkActorV1{{
			PrincipalId:   renewActorID,
			PrincipalKind: "agent",
			DelegationId:  "019f6bf7-3333-7333-8333-333333333371",
			GrantedScopes: []*basev0.WorkScopeV1{{
				ResourceKind: "evidence",
				Actions:      []string{"append", "read"},
			}},
		}},
	})
	require.NoError(t, err)
	return token
}

func TestRenewWorkContextExtendsDelegatedAuthorityForCurrentActor(t *testing.T) {
	server := newRenewTestServer(t)
	parent := mintDelegatedContext(t, server)
	parentClaims, err := server.verifier.Verify(parent, codefly.WorkContextExpectations{
		Issuer:   "accounts.test",
		TenantID: renewOrgID,
	})
	require.NoError(t, err)

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	issued, err := server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent.Encoded(),
		TtlSeconds:             900,
	})
	require.NoError(t, err)

	require.Equal(t, renewTaskID, issued.GetTaskId())
	require.Equal(t, renewSession, issued.GetSessionId())
	require.Equal(t, renewOwnerID, issued.GetOwnerPrincipalId())
	require.Equal(t, renewActorID, issued.GetCurrentActorPrincipalId())
	require.Equal(t, uint64(12), issued.GetAuthorizationRevision())
	// The whole point of the RPC: the renewed capability outlives the parent's
	// TTL cap without the owner present.
	require.True(t,
		issued.GetExpiresAt().AsTime().After(time.Unix(parentClaims.GetExpiresAtUnix(), 0)),
		"renewed Work Context must expire later than its parent",
	)

	renewed, err := codefly.ParseWorkContextToken(issued.GetToken())
	require.NoError(t, err)
	renewedClaims, err := server.verifier.Verify(renewed, codefly.WorkContextExpectations{
		Issuer:   "accounts.test",
		TenantID: renewOrgID,
	})
	require.NoError(t, err)
	require.Len(t, renewedClaims.GetActorChain(), 1)
	require.Equal(t, "tool.test", renewedClaims.GetAudience())
}

func TestRenewWorkContextRejectsScopeWidening(t *testing.T) {
	server := newRenewTestServer(t)
	parent := mintDelegatedContext(t, server)

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	_, err = server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent.Encoded(),
		AttenuatedScopes: []*gen.WorkContextScope{{
			ResourceKind: "evidence",
			Actions:      []string{"append", "read", "delete"},
		}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestRenewWorkContextRejectsAudienceOutsideActorCeiling proves the derive-path
// audience ceiling is enforced against the outermost actor that verifyActorParent
// resolves — i.e. the actor threaded out of requireCurrentAuthority, with no
// second authority resolution.
func TestRenewWorkContextRejectsAudienceOutsideActorCeiling(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority: &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{
			OrganizationRevision: 12,
			Actor: &business.Principal{
				ID:               renewActorID,
				Kind:             "agent",
				AgentIdentifier:  "pub/agent:1.0.0",
				AllowedAudiences: []string{"tool.test"},
			},
		}},
	})
	require.NoError(t, server.configureErr)
	parent := mintDelegatedContext(t, server)

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})

	forbidden := "forbidden.aud"
	_, err = server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent.Encoded(),
		Audience:               &forbidden,
		TtlSeconds:             900,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err),
		"an audience outside the actor's ceiling must be rejected")

	// Renewing without changing the audience (the parent's own, in-ceiling) works.
	_, err = server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent.Encoded(),
		TtlSeconds:             900,
	})
	require.NoError(t, err)
}

// TestRenewWorkContextRejectsScopeOutsideActorCeiling proves the derive-path
// enforces the SCOPE dimension of the actor ceiling, not only the audience. The
// requested scope ("evidence") is a valid, non-widening subset of the parent's
// own grant — so without the ceiling the renewal would succeed — but it is
// outside the actor's allowed_scopes ("repo"), which must reject it.
func TestRenewWorkContextRejectsScopeOutsideActorCeiling(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	newServer := func(allowedScopes []string) *WorkContextAuthorityServer {
		server := &WorkContextAuthorityServer{}
		server.Configure(WorkContextAuthorityConfiguration{
			Issuer:     "accounts.test",
			KeyID:      "accounts-test-key",
			PrivateKey: privateKey,
			Authority: &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{
				OrganizationRevision: 12,
				Actor: &business.Principal{
					ID:              renewActorID,
					Kind:            "agent",
					AgentIdentifier: "pub/agent:1.0.0",
					// Audience left unrestricted so the scope dimension is what fails.
					AllowedScopes: allowedScopes,
				},
			}},
		})
		require.NoError(t, server.configureErr)
		return server
	}

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	evidenceScope := []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"append", "read"}}}

	capped := newServer([]string{"repo"})
	_, err = capped.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: mintDelegatedContext(t, capped).Encoded(),
		AttenuatedScopes:       evidenceScope,
		TtlSeconds:             900,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err),
		"a resource kind outside the actor's ceiling must be rejected even when it does not widen the parent")

	// Same request, but the ceiling admits "evidence": the renewal proceeds.
	admits := newServer([]string{"evidence"})
	_, err = admits.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: mintDelegatedContext(t, admits).Encoded(),
		AttenuatedScopes:       evidenceScope,
		TtlSeconds:             900,
	})
	require.NoError(t, err)
}

func TestRenewWorkContextPreservesSingleUseReplayPolicy(t *testing.T) {
	server := newRenewTestServer(t)
	parent, _, err := server.signer.StartTask(codefly.StartTaskInput{
		Audience:              "tool.test",
		TenantID:              renewOrgID,
		OwnerPrincipalID:      renewOwnerID,
		TaskID:                renewTaskID,
		SessionID:             renewSession,
		AuthorizationRevision: 12,
		ReplayPolicy:          codefly.WorkContextReplaySingleUse,
		AuthorityScopes: []*basev0.WorkScopeV1{{
			ResourceKind: "evidence",
			Actions:      []string{"append"},
		}},
		ActorChain: []*basev0.WorkActorV1{{
			PrincipalId:   renewActorID,
			PrincipalKind: "agent",
			DelegationId:  "019f6bf7-3333-7333-8333-333333333372",
			GrantedScopes: []*basev0.WorkScopeV1{{
				ResourceKind: "evidence",
				Actions:      []string{"append"},
			}},
		}},
	})
	require.NoError(t, err)

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	// No replay_policy in the request: renewal must inherit SINGLE_USE, not
	// silently drop to idempotent.
	issued, err := server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent.Encoded(),
	})
	require.NoError(t, err)

	renewed, err := codefly.ParseWorkContextToken(issued.GetToken())
	require.NoError(t, err)
	renewedClaims, err := server.verifier.Verify(renewed, codefly.WorkContextExpectations{
		Issuer:   "accounts.test",
		TenantID: renewOrgID,
	})
	require.NoError(t, err)
	require.Equal(t, codefly.WorkContextReplaySingleUse, renewedClaims.GetReplayPolicy())
}

func TestVerifyActorParentBindsCallerToCurrentActor(t *testing.T) {
	server := newRenewTestServer(t)
	parent := mintDelegatedContext(t, server)

	_, claims, actor, err := server.verifyActorParent(
		context.Background(), renewOrgID, renewActorID, parent.Encoded(),
	)
	require.NoError(t, err)
	require.Equal(t, renewActorID, claims.GetActorChain()[0].GetPrincipalId())
	require.NotNil(t, actor, "the resolved outermost actor must be threaded back for ceiling enforcement")
	require.Equal(t, renewActorID, actor.ID)

	_, _, _, err = server.verifyActorParent(
		context.Background(), renewOrgID, renewOwnerID, parent.Encoded(),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestVerifyActorParentRejectsOwnerOnlyContext(t *testing.T) {
	server := newRenewTestServer(t)
	token, _, err := server.signer.StartTask(codefly.StartTaskInput{
		Audience:              "tool.test",
		TenantID:              renewOrgID,
		OwnerPrincipalID:      renewOwnerID,
		TaskID:                renewTaskID,
		SessionID:             renewSession,
		AuthorizationRevision: 12,
		ReplayPolicy:          codefly.WorkContextReplayIdempotent,
		AuthorityScopes: []*basev0.WorkScopeV1{{
			ResourceKind: "evidence",
			Actions:      []string{"append"},
		}},
	})
	require.NoError(t, err)

	_, _, _, err = server.verifyActorParent(
		context.Background(), renewOrgID, renewOwnerID, token.Encoded(),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestVerifyActorParentRejectsStaleRevision(t *testing.T) {
	server := newRenewTestServer(t)
	server.authority = &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{
		OrganizationRevision: 13,
		Actor:                &business.Principal{ID: renewActorID, Kind: "agent"},
	}}
	parent := mintDelegatedContext(t, server)

	_, _, _, err := server.verifyActorParent(
		context.Background(), renewOrgID, renewActorID, parent.Encoded(),
	)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
