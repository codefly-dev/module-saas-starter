package adapters

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"accounts/pkg/redisstate"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
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
