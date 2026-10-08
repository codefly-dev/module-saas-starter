package main

import (
	"context"
	"net"
	"testing"

	accountsv1 "auth-gateway/external/saas-starter/accounts"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type revisionOracleFixture struct {
	accountsv1.UnimplementedWorkContextServiceServer
	check func(context.Context, *accountsv1.CheckAuthorizationRevisionRequest) error
}

func (f *revisionOracleFixture) CheckAuthorizationRevision(ctx context.Context, r *accountsv1.CheckAuthorizationRevisionRequest) (*emptypb.Empty, error) {
	if err := f.check(ctx, r); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func TestCurrentWorkContextUsesGatewayOracleWithEverySignedSubject(t *testing.T) {
	scopes := []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}, ResourceIds: []string{"document-1"}}}
	claims := &basev0.WorkContextV1{TenantId: "tenant-1", OwnerPrincipalId: "owner-1", AuthorizationRevision: 42,
		AuthorityScopes: scopes, ActorChain: []*basev0.WorkActorV1{{PrincipalId: "actor-1", GrantedScopes: scopes}, {PrincipalId: "actor-2", GrantedScopes: scopes}},
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	calls := 0
	accountsv1.RegisterWorkContextServiceServer(server, &revisionOracleFixture{check: func(ctx context.Context, request *accountsv1.CheckAuthorizationRevisionRequest) error {
		calls++
		md, _ := metadata.FromIncomingContext(ctx)
		require.Equal(t, []string{"test-gateway-credential"}, md.Get("x-codefly-internal-token"))
		require.Empty(t, md.Get("authorization"))
		require.Empty(t, md.Get("x-user-id"))
		require.Equal(t, "tenant-1", request.OrgId)
		require.Equal(t, "owner-1", request.OwnerPrincipalId)
		require.Equal(t, uint64(42), request.AuthorizationRevision)
		require.Len(t, request.Subjects, 3)
		for i, id := range []string{"owner-1", "actor-1", "actor-2"} {
			require.Equal(t, id, request.Subjects[i].PrincipalId)
			require.True(t, proto.Equal(&accountsv1.WorkContextScope{ResourceKind: "documents", Actions: []string{"read"}, ResourceIds: []string{"document-1"}}, request.Subjects[i].Scopes[0]))
		}
		if calls > 1 {
			return status.Error(codes.PermissionDenied, "authority revoked")
		}
		return nil
	}})
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	t.Cleanup(func() { _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///authority-fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	authz := &ExtAuthz{backendConn: conn, internalToken: "test-gateway-credential"}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "caller-credential", "x-user-id", "forged-person"))
	require.NoError(t, authz.checkCurrentWorkContext(ctx, claims))
	require.Equal(t, codes.PermissionDenied, status.Code(authz.checkCurrentWorkContext(ctx, claims)))
	require.Equal(t, 2, calls, "current authority is neither cached nor retried")
	claims.ActorChain[1] = nil
	require.Equal(t, codes.PermissionDenied, status.Code(authz.checkCurrentWorkContext(ctx, claims)))
	require.Equal(t, 2, calls, "invalid subject must not reach the oracle")
	require.Equal(t, codes.Unavailable, status.Code((&ExtAuthz{}).checkCurrentWorkContext(ctx, claims)))
}
