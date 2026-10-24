package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"accounts/pkg/business"
)

type capturedLogs struct {
	mu   sync.Mutex
	logs []*wool.Log
}

func (c *capturedLogs) Process(l *wool.Log) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, l)
}

func (c *capturedLogs) only(t *testing.T) (*wool.Log, map[string]string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.logs, 1)
	fields := map[string]string{}
	for _, f := range c.logs[0].Fields {
		fields[f.Key] = fmt.Sprint(f.Value)
	}
	return c.logs[0], fields
}

func capturingContext(ctx context.Context) (context.Context, *capturedLogs) {
	sink := &capturedLogs{}
	return wool.New(ctx, &wool.Resource{Kind: "test", Unique: "refusals"}).WithLogger(sink).Inject(ctx), sink
}

const refusalProcedure = "/test.v1.Refusals/Refuse"

// serveRefusal serves one Connect procedure whose gRPC-style handler returns
// err, behind the production refusal interceptor, and captures its log.
func serveRefusal(t *testing.T, err error) (*httptest.Server, *capturedLogs) {
	t.Helper()
	sink := &capturedLogs{}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = wool.New(ctx, &wool.Resource{Kind: "test", Unique: "refusals"}).WithLogger(sink).Inject(ctx)
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(refusalProcedure, connect.NewUnaryHandler(refusalProcedure,
		func(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return unary(ctx, req, func(context.Context, *emptypb.Empty) (*emptypb.Empty, error) { return nil, err })
		},
		connect.WithInterceptors(inject, connectRefusalInterceptor()),
	))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, sink
}

func callRefusal(t *testing.T, server *httptest.Server) *connect.Error {
	t.Helper()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+refusalProcedure)
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	return ce
}

// The Connect message is the status's own message. The step-up prompt in the
// portal matches the reason exactly, so grpc-go's "rpc error: code = … desc =
// …" rendering on the wire is what kept it from ever appearing.
func TestConnectRefusalCarriesTheStatusMessage(t *testing.T) {
	server, sink := serveRefusal(t, status.Error(codes.FailedPrecondition, "mfa_required"))

	ce := callRefusal(t, server)
	require.Equal(t, connect.CodeFailedPrecondition, ce.Code())
	require.Equal(t, "mfa_required", ce.Message())

	line, fields := sink.only(t)
	require.Equal(t, wool.WARN, line.Level)
	require.Equal(t, "request refused", line.Message)
	require.Equal(t, refusalProcedure, fields["procedure"])
	require.Equal(t, "failed_precondition", fields["code"])
	require.Equal(t, "mfa_required", fields["reason"])
	require.NotContains(t, fields, "cause")

	// The wire body the browser reads. The frontend's step-up test decodes
	// exactly this body (src/features/auth/model/step-up.test.ts).
	response, err := server.Client().Post(server.URL+refusalProcedure, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"code":"failed_precondition","message":"mfa_required"}`, string(body))
}

// A fault that nobody mapped reaches the caller as Internal with a fixed
// sentence; the chain — here a constraint name — goes to the log only.
func TestConnectFaultWithholdsItsCause(t *testing.T) {
	fault := fmt.Errorf("CreateInvitation: cannot create invitation: %w", &pgconn.PgError{
		Code: "23505", Message: "duplicate key value violates unique constraint", ConstraintName: "idx_example_unique",
	})
	server, sink := serveRefusal(t, fault)

	ce := callRefusal(t, server)
	require.Equal(t, connect.CodeInternal, ce.Code())
	require.Equal(t, internalRefusalMessage, ce.Message())
	require.NotContains(t, ce.Error(), "constraint")

	line, fields := sink.only(t)
	require.Equal(t, wool.ERROR, line.Level)
	require.Equal(t, "internal", fields["code"])
	require.Equal(t, internalRefusalMessage, fields["reason"])
	require.Contains(t, fields["cause"], "duplicate key value violates unique constraint")
}

func TestBusinessRefusalsTravelUnderTheirCode(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		code    codes.Code
		message string
	}{
		"duplicate pending invitation": {
			fmt.Errorf("CreateInvitation: cannot create invitation: %w", business.ErrInvitationAlreadyPending),
			codes.AlreadyExists, business.ErrInvitationAlreadyPending.Error(),
		},
		"existing member": {
			fmt.Errorf("CreateInvitation: cannot create invitation: %w", business.ErrAlreadyMember),
			codes.AlreadyExists, business.ErrAlreadyMember.Error(),
		},
		"platform role, without the roles it compared": {
			fmt.Errorf("permission denied: %w: have support, need super_admin", business.ErrPlatformRoleRequired),
			codes.PermissionDenied, business.ErrPlatformRoleRequired.Error(),
		},
		"seat quota": {
			fmt.Errorf("AddOrgMember: %w", &business.EntitlementQuotaError{Feature: business.EntitlementSeats, Used: 5, Limit: 5}),
			codes.ResourceExhausted, "seats quota exhausted: used 5 of 5",
		},
		"caller went away": {
			fmt.Errorf("cannot read: %w", context.Canceled), codes.Canceled, "the request was canceled",
		},
		"already a status": {
			status.Error(codes.NotFound, "invitation not found"), codes.NotFound, "invitation not found",
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, cause := refusalStatus(tc.err)
			require.NoError(t, cause)
			require.Equal(t, tc.code, st.Code())
			require.Equal(t, tc.message, st.Message())

			var ce *connect.Error
			require.ErrorAs(t, translateGRPCError(tc.err), &ce)
			require.Equal(t, tc.message, ce.Message())
		})
	}
}

// The gRPC surface translates and logs the same way.
func TestGRPCRefusalInterceptorLogsAndWithholds(t *testing.T) {
	ctx, sink := capturingContext(context.Background())
	_, err := grpcRefusalInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/saas.accounts.v1.InvitationService/CreateInvitation"},
		func(context.Context, any) (any, error) { return nil, errors.New("pq: connection refused") })

	st := status.Convert(err)
	require.Equal(t, codes.Internal, st.Code())
	require.Equal(t, internalRefusalMessage, st.Message())

	line, fields := sink.only(t)
	require.Equal(t, wool.ERROR, line.Level)
	require.Equal(t, "/saas.accounts.v1.InvitationService/CreateInvitation", fields["procedure"])
	require.Equal(t, "pq: connection refused", fields["cause"])
}
