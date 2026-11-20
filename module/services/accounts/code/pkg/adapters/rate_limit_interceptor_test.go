package adapters

import (
	"context"
	"net"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"accounts/pkg/redisstate"
	"connectrpc.com/connect"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestGRPCRateLimitReportsWindowReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaultRateLimitConfig()
		time.Sleep(13 * time.Second)
		ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}})
		saved := rateLimiter
		defer WithRateLimiter(saved)
		WithRateLimiter(redisstate.NewRateLimiter(redisstate.NewMemory()))
		var reset time.Time
		for i := 0; i < cfg.defaultLimit; i++ {
			allowed, _, deadline, err := rateLimiter.Allow(ctx, grpcRateLimitKey(ctx), cfg.defaultLimit, cfg.defaultWindow)
			require.NoError(t, err)
			require.True(t, allowed)
			reset = deadline
		}
		_, err := grpcRateLimitInterceptor()(ctx, nil, nil, func(context.Context, any) (any, error) {
			t.Fatal("rate-limited call reached the handler")
			return nil, nil
		})
		st := status.Convert(err)
		require.Equal(t, codes.ResourceExhausted, st.Code())
		require.Len(t, st.Details(), 1)
		hint, ok := st.Details()[0].(*errdetails.RetryInfo)
		require.True(t, ok)
		require.Equal(t, time.Until(reset), hint.GetRetryDelay().AsDuration())
	})
}

func TestConnectRateLimitReportsWindowResetToGRPCClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaultRateLimitConfig()
		time.Sleep(13 * time.Second)
		limiter := redisstate.NewRateLimiter(redisstate.NewMemory())
		req := connect.NewRequest(&emptypb.Empty{})
		ctx := wool.WithOrgID(context.Background(), "qa-tenant")
		key := rateLimitKey(ctx, req)
		require.NotEmpty(t, key)
		var reset time.Time
		for i := 0; i < cfg.defaultLimit; i++ {
			allowed, _, deadline, err := limiter.Allow(ctx, key, cfg.defaultLimit, cfg.defaultWindow)
			require.NoError(t, err)
			require.True(t, allowed)
			reset = deadline
		}
		_, err := rateLimitInterceptor(limiter)(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			t.Fatal("rate-limited call reached the handler")
			return nil, nil
		})(ctx, req)
		var ce *connect.Error
		require.ErrorAs(t, err, &ce)
		require.Equal(t, connect.CodeResourceExhausted, ce.Code())
		require.Len(t, ce.Details(), 1)
		message, err := ce.Details()[0].Value()
		require.NoError(t, err)
		hint, ok := message.(*errdetails.RetryInfo)
		require.True(t, ok)
		require.Equal(t, time.Until(reset), hint.GetRetryDelay().AsDuration())
	})
}

// The gateway dials the Connect listener using gRPC, which reads the delay
// from the status details on the wire.
func TestGatewayTransportCanReadConnectLimitDelay(t *testing.T) {
	limiter := redisstate.NewRateLimiter(exhaustedWindow{})
	handler := connect.NewUnaryHandler(refusalProcedure, func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		t.Error("rate-limited request reached the handler")
		return nil, nil
	}, connect.WithInterceptors(connectRefusalInterceptor(), rateLimitInterceptor(limiter)))
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+refusalProcedure, connect.WithGRPC())
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	var refusal *connect.Error
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, connect.CodeResourceExhausted, refusal.Code())
	require.Len(t, refusal.Details(), 1)
	message, err := refusal.Details()[0].Value()
	require.NoError(t, err)
	hint, ok := message.(*errdetails.RetryInfo)
	require.True(t, ok)
	require.NotNil(t, hint.GetRetryDelay())
}

type exhaustedWindow struct{ redisstate.State }

func (exhaustedWindow) Incr(context.Context, string, time.Duration) (int64, error) {
	return int64(defaultRateLimitConfig().defaultLimit + 1), nil
}
