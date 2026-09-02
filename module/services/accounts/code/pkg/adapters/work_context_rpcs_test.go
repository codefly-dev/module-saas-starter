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

	_, _, err = server.verifyParent(
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
