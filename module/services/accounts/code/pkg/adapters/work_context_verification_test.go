package adapters

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/gen/saas/accounts/v1/accountsv1connect"
)

func verificationFixture(t *testing.T) (*WorkContextAuthorityServer, *sealAnswersFake, *workContextConsumerAuthorityFake, context.Context, *gen.VerifyIdempotentWorkContextRequest) {
	t.Helper()
	SetInternalToken("verification-test-perimeter")
	t.Cleanup(func() { SetInternalToken("") })
	facts := &workContextConsumerAuthorityFake{}
	seals := newSealAnswersFake(12)
	server := newCoreTestServer(t, facts, seals)
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}, ResourceIds: []string{"one"}}}
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: "example", Tenant: uuid.NewString(), Owner: uuid.NewString(), Scopes: scopes,
		Actors: []delegationHop{agentHop(uuid.NewString(), uuid.NewString(), scopes...)},
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "verification-test-perimeter"))
	return server, seals, facts, ctx, &gen.VerifyIdempotentWorkContextRequest{
		WorkContextToken: token, ExpectedIssuer: "accounts.test", ExpectedAudience: "example",
	}
}

func TestVerifyIdempotentTransportProjectsVerifiedEffectiveAuthority(t *testing.T) {
	server, _, facts, _, input := verificationFixture(t)
	_, handler := accountsv1connect.NewWorkContextServiceHandler(&workContextConnectHandler{inner: server})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	client := accountsv1connect.NewWorkContextServiceClient(httpServer.Client(), httpServer.URL)
	req := connect.NewRequest(input)
	_, err := client.VerifyIdempotent(context.Background(), req)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	req.Header().Set("X-Codefly-Internal-Token", "verification-test-perimeter")
	out, err := client.VerifyIdempotent(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, facts.revisionOrg, out.Msg.TenantId)
	require.Equal(t, facts.revisionOwner, out.Msg.OwnerPrincipalId)
	require.Len(t, facts.revisionSubjects, 2, "current permissions of every subject must be rechecked")
	require.Equal(t, facts.revisionSubjects[1].PrincipalID, out.Msg.CurrentActorPrincipalId)
	require.NotEmpty(t, out.Msg.TaskId)
	require.True(t, out.Msg.ExpiresAt.AsTime().After(time.Now()))
	require.Len(t, out.Msg.EffectiveScopes, 1)
	require.Equal(t, []string{"one"}, out.Msg.EffectiveScopes[0].ResourceIds)
	require.Equal(t, []string{"read"}, out.Msg.EffectiveScopes[0].Actions)
	require.Empty(t, server.replay.(*memoryReplay).seen)
}

func TestVerifyIdempotentRechecksLiveStateAndPermissions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*WorkContextAuthorityServer, *sealAnswersFake, *workContextConsumerAuthorityFake, *gen.VerifyIdempotentWorkContextRequest)
		code   codes.Code
	}{
		{"revision", func(_ *WorkContextAuthorityServer, s *sealAnswersFake, _ *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			s.setRevision(13)
		}, codes.FailedPrecondition},
		{"installation", func(_ *WorkContextAuthorityServer, s *sealAnswersFake, _ *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			s.unheld[testInstallation("example")] = true
		}, codes.FailedPrecondition},
		{"actor epoch", func(_ *WorkContextAuthorityServer, s *sealAnswersFake, f *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			s.epochs[f.revisionSubjects[1].PrincipalID] = 2
		}, codes.FailedPrecondition},
		{"owner epoch", func(_ *WorkContextAuthorityServer, s *sealAnswersFake, f *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			s.epochs[f.revisionOwner] = 2
		}, codes.FailedPrecondition},
		{"removed actor", func(_ *WorkContextAuthorityServer, s *sealAnswersFake, f *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			s.unknown[f.revisionSubjects[1].PrincipalID] = true
		}, codes.FailedPrecondition},
		{"removed owner", func(_ *WorkContextAuthorityServer, s *sealAnswersFake, f *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			s.unknown[f.revisionOwner] = true
		}, codes.FailedPrecondition},
		{"permissions", func(_ *WorkContextAuthorityServer, _ *sealAnswersFake, f *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			f.revisionErr = business.ErrWorkContextAuthorizationStale
		}, codes.FailedPrecondition},
		{"unavailable", func(_ *WorkContextAuthorityServer, _ *sealAnswersFake, f *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			f.revisionErr = errors.New("unavailable")
		}, codes.Unavailable},
		{"expiry", func(s *WorkContextAuthorityServer, _ *sealAnswersFake, _ *workContextConsumerAuthorityFake, _ *gen.VerifyIdempotentWorkContextRequest) {
			s.now = func() time.Time { return time.Now().Add(time.Hour) }
		}, codes.InvalidArgument},
		{"audience", func(_ *WorkContextAuthorityServer, _ *sealAnswersFake, _ *workContextConsumerAuthorityFake, r *gen.VerifyIdempotentWorkContextRequest) {
			r.ExpectedAudience = "other"
		}, codes.InvalidArgument},
		{"issuer", func(_ *WorkContextAuthorityServer, _ *sealAnswersFake, _ *workContextConsumerAuthorityFake, r *gen.VerifyIdempotentWorkContextRequest) {
			r.ExpectedIssuer = "other"
		}, codes.InvalidArgument},
		{"malformed", func(_ *WorkContextAuthorityServer, _ *sealAnswersFake, _ *workContextConsumerAuthorityFake, r *gen.VerifyIdempotentWorkContextRequest) {
			r.WorkContextToken = "not-a-capability"
		}, codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, seals, facts, ctx, req := verificationFixture(t)
			_, err := server.VerifyIdempotent(ctx, req)
			require.NoError(t, err)
			test.change(server, seals, facts, req)
			out, err := server.VerifyIdempotent(ctx, req)
			require.Nil(t, out)
			require.Equal(t, test.code, status.Code(err), "%v", err)
			require.Empty(t, server.replay.(*memoryReplay).seen)
		})
	}
}

func TestVerifyIdempotentNeverConsumesUnsupportedReplay(t *testing.T) {
	server, _, _, ctx, req := verificationFixture(t)
	token, _ := mintTestCapability(t, server, testCapability{
		Audience: "example", Tenant: uuid.NewString(), Owner: uuid.NewString(),
		Replay: workcontext.ReplaySingleUse,
		Scopes: []*basev0.WorkScopeV1{{ResourceKind: "evidence", Actions: []string{"read"}}},
	})
	req.WorkContextToken = token
	for range 2 {
		_, err := server.VerifyIdempotent(ctx, req)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Empty(t, server.replay.(*memoryReplay).seen)
	}
	_, err := server.verifyFor(ctx, token, "example")
	require.NoError(t, err, "the actual consumer must still be able to spend it")
	_, err = server.verifyFor(ctx, token, "example")
	require.ErrorIs(t, err, workcontext.ErrReplayed)
}
