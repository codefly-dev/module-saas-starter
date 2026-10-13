package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type workContextAuthorityFake struct {
	facts            *business.WorkContextAuthorityFacts
	err              error
	installationFcts *business.InstallationAuthorityFacts
	installationErr  error
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

// SolutionRuntimeBoundarySeed and SolutionRuntimeBoundarySeeds model a
// deployment with NO registered solution: there is no boundary to be given and
// none to collide with. They are on the base fake because the mint's collision
// check fails CLOSED on a store that cannot answer (issue #1015) — a nil one is
// a wiring bug, not an empty registry — so every test that mints has to say
// which of the two it is, and these say "empty registry".
func (f *workContextAuthorityFake) SolutionRuntimeBoundarySeed(
	_ context.Context, _ string,
) (business.SolutionBoundarySeed, error) {
	return business.SolutionBoundarySeed{}, business.ErrSolutionRegistrationNotFound
}

func (f *workContextAuthorityFake) SolutionRuntimeBoundarySeeds(_ context.Context) ([]string, error) {
	return nil, nil
}

func (f *workContextAuthorityFake) ResolveInstallationAuthority(
	_ context.Context,
	_ string,
	_ string,
	_ []business.WorkContextPermission,
) (*business.InstallationAuthorityFacts, error) {
	return f.installationFcts, f.installationErr
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
		Seals:      newSealAnswersFake(12),
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
		Seals:      newSealAnswersFake(12),
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
		Seals:      newSealAnswersFake(12),
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
	}}, noContentReads)
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
		Seals:      newSealAnswersFake(12),
	})
	require.NoError(t, server.configureErr)

	token, _ := mintTestCapability(t, server, testCapability{
		Audience: "consumer.test",
		Tenant:   "019f6bf7-5b4b-74e5-8c17-092259bb1661",
		Owner:    "019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
		Task:     "019f6bf7-1111-7111-8111-111111111111",
		Scopes:   []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"append"}}},
	})

	// Core verifies the capability against the revision it was minted at (12);
	// the host's own recheck finds the owner's authority moved to 13.
	_, _, err = server.verifyParent(
		context.Background(),
		"019f6bf7-5b4b-74e5-8c17-092259bb1661",
		"019f6bf7-5b1c-730d-9687-fe6d4aff31ed",
		token,
	)
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestWorkContextAuthorityConfigurationFailsClosed(t *testing.T) {
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{})
	require.Error(t, server.configureErr)
	require.Nil(t, server.key)
	require.Nil(t, server.seals)
	require.Nil(t, server.authority)
	require.False(t, server.configured())
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
		Seals:      newSealAnswersFake(12),
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

// The host's audience vocabulary is derived, and every mint now resolves it
// (business.RequireHostAudience), so a Store that cannot answer this read fails
// every mint closed — which is the right production behaviour and would make these
// tests fail on the vocabulary instead of on the mint they are about.
//
// This store declares NO solution bindings. The audiences these tests mint for are
// declared MODULE prefixes, the set's third member, installed by
// declareTestAudiences below — which is what they are in a real deployment too:
// a forwarded viewer context names the module it was minted for.
func (renewMembershipStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (renewMembershipStore) LiveDeclaredBindings(context.Context) ([]business.LiveDeclaredBinding, error) {
	return nil, nil
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
		Seals: newSealAnswersFake(12),
	})
	require.NoError(t, server.configureErr)
	server.replay = &memoryReplay{}
	return server
}

// mintDelegatedContext issues a Work Context whose current actor is renewActorID,
// modelling a context an in-flight agent holds while acting for the owner.
func mintDelegatedContext(t *testing.T, server *WorkContextAuthorityServer) string {
	t.Helper()
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: "tool.test",
		Tenant:   renewOrgID,
		Owner:    renewOwnerID,
		Task:     renewTaskID,
		Session:  renewSession,
		Scopes:   []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"append", "read"}}},
		Actors: []delegationHop{agentHop(renewActorID, "019f6bf7-3333-7333-8333-333333333371",
			&basev0.WorkScopeV1{ResourceKind: "evidence", Actions: []string{"append", "read"}})},
	})
	return token
}

func TestRenewWorkContextExtendsDelegatedAuthorityForCurrentActor(t *testing.T) {
	server := newRenewTestServer(t)
	parent := mintDelegatedContext(t, server)
	parentClaims, err := server.verifyIssued(context.Background(), parent)
	require.NoError(t, err)

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	issued, err := server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent,
		TtlSeconds:             900,
	})
	require.NoError(t, err)

	require.Equal(t, renewTaskID, issued.GetTaskId())
	// A renewal opens a fresh Session: the caller is the actor, and a session
	// this issuer did not authenticate the caller in is never carried over.
	require.NotEqual(t, renewSession, issued.GetSessionId())
	require.NotEmpty(t, issued.GetSessionId())
	require.Equal(t, renewOwnerID, issued.GetOwnerPrincipalId())
	require.Equal(t, renewActorID, issued.GetCurrentActorPrincipalId())
	require.Equal(t, uint64(12), issued.GetAuthorizationRevision())
	// The whole point of the RPC: the renewed capability outlives the parent's
	// TTL cap without the owner present.
	require.True(t,
		issued.GetExpiresAt().AsTime().After(time.Unix(parentClaims.Context().GetExpiresAtUnix(), 0)),
		"renewed Work Context must expire later than its parent",
	)

	renewed, err := server.verifyIssued(context.Background(), issued.GetToken())
	require.NoError(t, err)
	require.Len(t, renewed.Context().GetActorChain(), 1)
	require.Equal(t, "019f6bf7-3333-7333-8333-333333333371", renewed.Context().GetActorChain()[0].GetDelegationId(),
		"the delegation the actor holds is the one re-derived, not a new one")
	require.Equal(t, "tool.test", renewed.Context().GetAudience())
}

func TestRenewWorkContextRejectsScopeWidening(t *testing.T) {
	server := newRenewTestServer(t)
	parent := mintDelegatedContext(t, server)

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	_, err = server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent,
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
		Seals: newSealAnswersFake(12),
	})
	require.NoError(t, server.configureErr)
	parent := mintDelegatedContext(t, server)

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})

	forbidden := "forbidden.aud"
	_, err = server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent,
		Audience:               &forbidden,
		TtlSeconds:             900,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err),
		"an audience outside the actor's ceiling must be rejected")

	// Renewing without changing the audience (the parent's own, in-ceiling) works.
	_, err = server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent,
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
			Seals: newSealAnswersFake(12),
		})
		require.NoError(t, server.configureErr)
		return server
	}

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	evidenceScope := []*gen.WorkContextScope{{ResourceKind: "evidence", Actions: []string{"append", "read"}}}

	capped := newServer([]string{"repo"})
	_, err = capped.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: mintDelegatedContext(t, capped),
		AttenuatedScopes:       evidenceScope,
		TtlSeconds:             900,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err),
		"a resource kind outside the actor's ceiling must be rejected even when it does not widen the parent")

	// Same request, but the ceiling admits "evidence": the renewal proceeds.
	admits := newServer([]string{"evidence"})
	_, err = admits.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: mintDelegatedContext(t, admits),
		AttenuatedScopes:       evidenceScope,
		TtlSeconds:             900,
	})
	require.NoError(t, err)
}

func TestRenewWorkContextPreservesSingleUseReplayPolicy(t *testing.T) {
	server := newRenewTestServer(t)
	parent, _ := mintTestCapability(t, server, testCapability{
		Audience: "tool.test",
		Tenant:   renewOrgID,
		Owner:    renewOwnerID,
		Task:     renewTaskID,
		Replay:   workcontext.ReplaySingleUse,
		Scopes:   []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"append"}}},
		Actors: []delegationHop{agentHop(renewActorID, "019f6bf7-3333-7333-8333-333333333372",
			&basev0.WorkScopeV1{ResourceKind: "evidence", Actions: []string{"append"}})},
	})

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	ctx := stampVerifiedIdentity(context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{})
	// No replay_policy in the request: renewal must inherit SINGLE_USE, not
	// silently drop to idempotent.
	issued, err := server.RenewWorkContext(ctx, &gen.RenewWorkContextRequest{
		OrgId:                  renewOrgID,
		ParentWorkContextToken: parent,
	})
	require.NoError(t, err)

	renewed, err := workcontext.Decode(issued.GetToken())
	require.NoError(t, err)
	require.Equal(t, workcontext.ReplaySingleUse, renewed.Context().GetReplayPolicy())
}

func TestVerifyActorParentBindsCallerToCurrentActor(t *testing.T) {
	server := newRenewTestServer(t)
	parent := mintDelegatedContext(t, server)

	verified, actor, err := server.verifyActorParent(
		context.Background(), renewOrgID, renewActorID, parent,
	)
	require.NoError(t, err)
	require.Equal(t, renewActorID, verified.Context().GetActorChain()[0].GetPrincipalId())
	require.NotNil(t, actor, "the resolved outermost actor must be threaded back for ceiling enforcement")
	require.Equal(t, renewActorID, actor.ID)

	_, _, err = server.verifyActorParent(
		context.Background(), renewOrgID, renewOwnerID, parent,
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestVerifyActorParentRejectsOwnerOnlyContext(t *testing.T) {
	server := newRenewTestServer(t)
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: "tool.test",
		Tenant:   renewOrgID,
		Owner:    renewOwnerID,
		Task:     renewTaskID,
		Scopes:   []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"append"}}},
	})

	_, _, err := server.verifyActorParent(
		context.Background(), renewOrgID, renewOwnerID, token,
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestStartTaskRootsTaskInCallerVerifiedSession(t *testing.T) {
	const foreignSession = "019f6bf7-2222-7222-8222-222222222272"

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	request := func(sessionID string) *gen.StartTaskWorkContextRequest {
		return &gen.StartTaskWorkContextRequest{
			OrgId:            renewOrgID,
			TaskId:           renewTaskID,
			ActorPrincipalId: renewActorID,
			Audience:         "tool.test",
			TtlSeconds:       900,
			AuthorityScopes: []*gen.WorkContextScope{{
				ResourceKind: "evidence",
				Actions:      []string{"read"},
			}},
		}
	}
	caller := func() context.Context {
		return stampVerifiedIdentity(
			context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{},
		)
	}

	t.Run("session_backed_caller", func(t *testing.T) {
		server := newRenewTestServer(t)
		ctx := accountsauth.WithVerifiedSessionIDString(caller(), renewSession)

		issued, err := server.StartTask(ctx, request(renewSession))
		require.NoError(t, err)
		require.Equal(t, renewSession, issued.GetSessionId())
	})

	// A caller that roots its own Task and Session ids while presenting a real
	// session must keep minting, and must still get a capability bound to the
	// session it actually holds rather than the one it named. Refusing here
	// instead would have bound nothing extra and broken that caller outright.
	t.Run("session_the_caller_does_not_hold_is_ignored_not_refused", func(t *testing.T) {
		server := newRenewTestServer(t)
		ctx := accountsauth.WithVerifiedSessionIDString(caller(), renewSession)

		issued, err := server.StartTask(ctx, request(foreignSession))
		require.NoError(t, err)
		require.Equal(t, renewSession, issued.GetSessionId())
	})

	// A caller with no verified session gets a FRESH one, never the id it named:
	// a requested id signed as-is would let a caller mint a capability carrying
	// another credential's session, and the gateway pairs a confined credential
	// with a capability on the session alone.
	t.Run("caller_without_a_session_gets_a_fresh_one_not_the_requested_id", func(t *testing.T) {
		server := newRenewTestServer(t)

		issued, err := server.StartTask(caller(), request(foreignSession))
		require.NoError(t, err)
		require.NotEqual(t, foreignSession, issued.GetSessionId())
		require.NotEmpty(t, issued.GetSessionId())
	})
}

// mintOwnedContext issues a Work Context the caller owns outright, rooted in the
// given Session — the capability a StartRootSession call derives from.
func mintOwnedContext(
	t *testing.T, server *WorkContextAuthorityServer, sessionID string,
) string {
	t.Helper()
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: "tool.test",
		Tenant:   renewOrgID,
		Owner:    renewActorID,
		Task:     renewTaskID,
		Session:  sessionID,
		Scopes:   []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"append", "read"}}},
	})
	return token
}

func TestStartRootSessionGeneratesFreshRootSession(t *testing.T) {
	const requestedSession = "019f6bf7-2222-7222-8222-222222222274"

	previous := service
	svc, err := business.NewService(renewMembershipStore{})
	require.NoError(t, err)
	service = svc
	declareTestAudiences(svc)
	t.Cleanup(func() { service = previous })

	request := func(parent string, sessionID string) *gen.StartRootSessionWorkContextRequest {
		return &gen.StartRootSessionWorkContextRequest{
			OrgId:                  renewOrgID,
			ParentWorkContextToken: parent,
			SessionId:              sessionID,
			Audience:               "tool.test",
			TtlSeconds:             900,
		}
	}
	caller := func() context.Context {
		return stampVerifiedIdentity(
			context.Background(), renewActorID, renewOrgID, accountsauth.Assurance{},
		)
	}

	// The id a request names never reaches the capability: a root Session keeps
	// no lineage, so a caller-chosen one is free to name a Session that never
	// existed or one belonging to unrelated work.
	t.Run("requested_session_id_is_not_honoured", func(t *testing.T) {
		server := newRenewTestServer(t)
		parent := mintOwnedContext(t, server, renewSession)

		issued, err := server.StartRootSession(caller(), request(parent, requestedSession))
		require.NoError(t, err)
		require.NotEqual(t, requestedSession, issued.GetSessionId())
		require.NotEqual(t, renewSession, issued.GetSessionId())
		require.Equal(t, renewTaskID, issued.GetTaskId())
		require.Equal(t, renewActorID, issued.GetOwnerPrincipalId())
	})

	// A Work Context Session is an execution scope, not the caller's auth
	// session: this issuer owns no consumer Session rows. Deriving the auth
	// session refused exactly this call — the ordinary one, since the parent Task
	// is rooted in the session the caller still holds.
	t.Run("caller_in_the_parent_session_still_succeeds", func(t *testing.T) {
		server := newRenewTestServer(t)
		ctx := accountsauth.WithVerifiedSessionIDString(caller(), renewSession)
		parent := mintOwnedContext(t, server, renewSession)

		issued, err := server.StartRootSession(ctx, request(parent, renewSession))
		require.NoError(t, err)
		require.NotEqual(t, renewSession, issued.GetSessionId())
	})

	// Freshness is the property a caller cannot be trusted to supply, so two
	// mints from one parent must never land on the same root Session.
	t.Run("each_call_mints_a_distinct_root_session", func(t *testing.T) {
		server := newRenewTestServer(t)
		parent := mintOwnedContext(t, server, renewSession)

		first, err := server.StartRootSession(caller(), request(parent, requestedSession))
		require.NoError(t, err)
		second, err := server.StartRootSession(caller(), request(parent, requestedSession))
		require.NoError(t, err)
		require.NotEqual(t, first.GetSessionId(), second.GetSessionId())
	})

	// uuid validation accepts uppercase hex, so a parent Session can be spelled
	// in a form an equality check against a canonical id would miss. A generated
	// id cannot alias the parent under any spelling.
	t.Run("parent_session_in_another_spelling_cannot_be_aliased", func(t *testing.T) {
		server := newRenewTestServer(t)
		upper := strings.ToUpper(renewSession)
		parent := mintOwnedContext(t, server, upper)

		issued, err := server.StartRootSession(caller(), request(parent, renewSession))
		require.NoError(t, err)
		require.NotEqual(t, upper, issued.GetSessionId())
		require.NotEqual(t, renewSession, issued.GetSessionId())
	})
}

func TestVerifyActorParentRejectsStaleRevision(t *testing.T) {
	server := newRenewTestServer(t)
	server.authority = &workContextAuthorityFake{facts: &business.WorkContextAuthorityFacts{
		OrganizationRevision: 13,
		Actor:                &business.Principal{ID: renewActorID, Kind: "agent"},
	}}
	parent := mintDelegatedContext(t, server)

	_, _, err := server.verifyActorParent(
		context.Background(), renewOrgID, renewActorID, parent,
	)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
