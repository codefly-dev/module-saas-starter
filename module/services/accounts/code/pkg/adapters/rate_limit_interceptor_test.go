package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"accounts/pkg/redisstate"
	"connectrpc.com/connect"
	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
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
			allowed, _, deadline, err := rateLimiter.Allow(ctx, ipRateLimitKey("127.0.0.1"), cfg.defaultLimit, cfg.defaultWindow)
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
		key := rateLimitKeyFromCtx(ctx)
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

type failingCounter struct{ redisstate.State }

var errCounterOutage = errors.New("dial tcp 10.0.0.9:6379: connect: connection refused")

func (failingCounter) Incr(context.Context, string, time.Duration) (int64, error) {
	return 0, errCounterOutage
}

// A hit that cannot be counted is refused. Admitting it lifted every budget,
// sign-in and registration included, for as long as the store was down.
func TestConnectRateLimitRefusesWhenTheCounterFails(t *testing.T) {
	limiter := redisstate.NewRateLimiter(failingCounter{})
	ctx, sink := capturingContext(wool.WithOrgID(context.Background(), "qa-tenant"))
	_, err := rateLimitInterceptor(limiter)(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("an uncounted call reached the handler")
		return nil, nil
	})(ctx, connect.NewRequest(&emptypb.Empty{}))

	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	require.Equal(t, connect.CodeUnavailable, ce.Code())
	require.Equal(t, rateLimitUnavailableMessage, ce.Message())
	require.Equal(t, "5", ce.Meta().Get("Retry-After"))
	require.Len(t, ce.Details(), 1)
	message, err := ce.Details()[0].Value()
	require.NoError(t, err)
	hint, ok := message.(*errdetails.RetryInfo)
	require.True(t, ok)
	require.Equal(t, rateLimitUnavailableRetry, hint.GetRetryDelay().AsDuration())

	// The caller is told nothing of the store; the operator is told the cause.
	require.NotContains(t, ce.Message(), "6379")
	line, fields := sink.only(t)
	require.Equal(t, wool.WARN, line.Level)
	require.Equal(t, rateLimitUnavailableLog, line.Message)
	require.Contains(t, fmt.Sprint(fields), errCounterOutage.Error())
}

func TestGRPCRateLimitRefusesWhenTheCounterFails(t *testing.T) {
	saved := rateLimiter
	defer WithRateLimiter(saved)
	WithRateLimiter(redisstate.NewRateLimiter(failingCounter{}))
	ctx, sink := capturingContext(peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}}))
	_, err := grpcRateLimitInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/host.session.v1.SessionService/SignIn"}, func(context.Context, any) (any, error) {
		t.Fatal("an uncounted call reached the handler")
		return nil, nil
	})

	st := status.Convert(err)
	require.Equal(t, codes.Unavailable, st.Code())
	require.Equal(t, rateLimitUnavailableMessage, st.Message())
	require.Len(t, st.Details(), 1)
	hint, ok := st.Details()[0].(*errdetails.RetryInfo)
	require.True(t, ok)
	require.Equal(t, rateLimitUnavailableRetry, hint.GetRetryDelay().AsDuration())
	line, _ := sink.only(t)
	require.Equal(t, wool.WARN, line.Level)
	require.Equal(t, rateLimitUnavailableLog, line.Message)
}

// Over the wire the refusal is 503 with Retry-After, and the refusal
// interceptor outside it neither rewrites it nor logs it a second time as a
// fault of the host.
func TestCounterOutageIs503OnTheWire(t *testing.T) {
	limiter := redisstate.NewRateLimiter(failingCounter{})
	handler := connect.NewUnaryHandler(refusalProcedure, func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		t.Error("an uncounted request reached the handler")
		return nil, nil
	}, connect.WithInterceptors(connectRefusalInterceptor(), rateLimitInterceptor(limiter)))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	response, err := server.Client().Post(server.URL+refusalProcedure, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.Equal(t, "5", response.Header.Get("Retry-After"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), rateLimitUnavailableMessage)
	require.NotContains(t, string(body), "6379")
}

// Every workload of a cell reaches the authority from one address (loopback,
// or a mesh proxy), and one person's action costs a Check per hop. Counted
// in the anonymous visitor's budget, a busy minute refused every module's
// reads; a visitor spending that budget must not refuse them either.
func TestAuthorityChecksAreNotChargedToTheAnonymousBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaultRateLimitConfig()
		time.Sleep(13 * time.Second)
		ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}})
		saved := rateLimiter
		defer WithRateLimiter(saved)
		WithRateLimiter(redisstate.NewRateLimiter(redisstate.NewMemory()))
		call := func(procedure string) error {
			_, err := grpcRateLimitInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: procedure}, func(context.Context, any) (any, error) {
				return nil, nil
			})
			return err
		}
		const check = "/host.authority.v1.AuthorityService/Check"
		const signIn = "/host.session.v1.SessionService/SignIn"
		for i := 0; i < cfg.defaultLimit; i++ {
			require.NoError(t, call(signIn))
		}
		require.Equal(t, codes.ResourceExhausted, status.Code(call(signIn)))
		// The checks of the same minute are admitted, well past the anonymous budget.
		for i := 0; i < 2*cfg.defaultLimit; i++ {
			require.NoError(t, call(check))
		}
		// The machine budget is still a budget.
		for i := 2 * cfg.defaultLimit; i < cfg.machineLimit; i++ {
			require.NoError(t, call(check))
		}
		require.Equal(t, codes.ResourceExhausted, status.Code(call(check)))
	})
}

func TestRateLimitBudgetNamesTheCaller(t *testing.T) {
	cfg := defaultRateLimitConfig()
	const check = "/host.authority.v1.AuthorityService/Check"
	// A verified identity is the bucket, whatever the procedure.
	require.Equal(t, rateLimitBudget{key: "auth:dana", limit: cfg.defaultLimit}, rateLimitBudgetFor(cfg, "auth:dana", check, "10.0.0.1"))
	require.Equal(t, rateLimitBudget{key: "machine:10.0.0.1", limit: cfg.machineLimit}, rateLimitBudgetFor(cfg, "", check, "10.0.0.1"))
	require.Equal(t, rateLimitBudget{key: "ip:10.0.0.1", limit: cfg.defaultLimit}, rateLimitBudgetFor(cfg, "", "/saas.accounts.v1.WaitlistService/JoinWaitlist", "10.0.0.1"))
	require.Equal(t, rateLimitBudget{}, rateLimitBudgetFor(cfg, "", check, ""))
}
