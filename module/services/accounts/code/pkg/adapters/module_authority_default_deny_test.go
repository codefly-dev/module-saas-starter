package adapters

import (
	"context"
	"net"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Every procedure the module authority endpoint serves is called, on a server
// built exactly as the endpoint is (its interceptors and the catalog's
// registrations), by a caller that proves no module: first with no credential,
// then with the internal token alone. Each must be refused —
// Unauthenticated or PermissionDenied — before anything is written. A method
// added to the endpoint later is in ModuleAuthorityProcedures, so it is called
// here too: it cannot be served open by forgetting to identify its caller.
func TestEveryModuleAuthorityProcedureRefusesAnUnidentifiedCaller(t *testing.T) {
	store := installModuleWorkContextService(t, documentsPrincipals)
	installModuleWorkContextAuthority(t)
	previousToken := internalToken
	SetInternalToken("default-deny-test-token")
	t.Cleanup(func() { SetInternalToken(previousToken) })

	server := grpc.NewServer(
		grpc.ChainUnaryInterceptor(grpcRefusalInterceptor(), grpcAuthInterceptor(nil, rpcExposureModule)),
		grpc.ChainStreamInterceptor(grpcStreamAuthInterceptor(nil, rpcExposureModule)),
	)
	registerCatalogGRPCServices(server, &GrpcServer{
		User: &UserServer{}, Org: &OrgServer{}, Team: &TeamServer{}, Perm: &PermServer{},
		Ident: &IdentServer{}, APIKey: &APIKeyServer{}, Auth: &AuthServer{}, MFA: &MFAServer{},
		Audit: &AuditServer{}, Invitation: &InvitationServer{}, PlatformAdmin: &PlatformAdminServer{},
		Introspection: &IntrospectionServer{},
	})
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///module-authority",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	callers := map[string]context.Context{
		"no credential":            context.Background(),
		"the internal token alone": metadata.AppendToOutgoingContext(context.Background(), "x-codefly-internal-token", "default-deny-test-token"),
	}
	procedures := business.ModuleAuthorityProcedures()
	require.NotEmpty(t, procedures)
	for _, procedure := range procedures {
		method := methodDescriptor(t, procedure)
		for name, ctx := range callers {
			t.Run(strings.TrimPrefix(procedure, "/")+"/"+name, func(t *testing.T) {
				before := store.recordAttempts()
				err := callWithNoModule(ctx, conn, procedure, method)
				code := status.Code(err)
				require.Truef(t, code == codes.Unauthenticated || code == codes.PermissionDenied,
					"%s answered %v to a caller with %s; it must refuse: %v", procedure, code, name, err)
				require.Equal(t, before, store.recordAttempts(), "%s wrote before refusing", procedure)
			})
		}
	}
}

func methodDescriptor(t *testing.T, procedure string) protoreflect.MethodDescriptor {
	t.Helper()
	service, method, ok := strings.Cut(strings.TrimPrefix(procedure, "/"), "/")
	require.True(t, ok, procedure)
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service + "." + method))
	require.NoError(t, err, procedure)
	return descriptor.(protoreflect.MethodDescriptor)
}

// callWithNoModule makes one call that carries no module Work Context. Its
// request is empty, except for a workload mint on the allowlist, which is
// given a well-formed request with a credential that proves nothing, so the
// handler's own refusal is what is asserted rather than its input validation.
func callWithNoModule(ctx context.Context, conn *grpc.ClientConn, procedure string, method protoreflect.MethodDescriptor) error {
	var request proto.Message = dynamicpb.NewMessage(method.Input())
	if business.ModuleAuthorityAuthenticatesInRequest(procedure) {
		request = allowlistedRequest(procedure)
	}
	response := dynamicpb.NewMessage(method.Output())
	if !method.IsStreamingClient() && !method.IsStreamingServer() {
		return conn.Invoke(ctx, procedure, request, response)
	}
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{
		ServerStreams: method.IsStreamingServer(), ClientStreams: method.IsStreamingClient(),
	}, procedure)
	if err != nil {
		return err
	}
	if err := stream.SendMsg(request); err != nil {
		return stream.RecvMsg(response)
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	return stream.RecvMsg(response)
}

func allowlistedRequest(procedure string) proto.Message {
	switch procedure {
	case "/saas.accounts.v1.ModuleCapabilitiesService/MintSourceOperationContext":
		return &gen.ModuleMintSourceOperationContextRequest{
			Prefix: "documents", ServiceAccountToken: "not-a-projected-token",
			Delegation: &gen.ModuleMintSourceOperationContextRequest_SourceId{SourceId: "019f6bf7-5555-7555-8555-555555555555"},
		}
	}
	panic("allowlisted procedure " + procedure + " has no request in allowlistedRequest; add one so the test calls it")
}
