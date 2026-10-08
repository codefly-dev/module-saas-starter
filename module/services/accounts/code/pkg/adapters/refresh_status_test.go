package adapters

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type refreshOutcomeMinter struct {
	auth.JWTMinter
	err error
}

func (m refreshOutcomeMinter) VerifyRefresh(context.Context, string) (*auth.TokenPair, error) {
	return nil, m.err
}

// Exercise the actual business and RPC path: rejected credentials must tell
// clients to sign in, while infrastructure failures must remain retryable.
func TestRefreshRejectionIsUnauthenticated(t *testing.T) {
	previous := service
	t.Cleanup(func() { service = previous })
	for _, tc := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{"revoked", auth.ErrRefreshRevoked, codes.Unauthenticated},
		{"reused", auth.ErrRefreshReuse, codes.Unauthenticated},
		{"wrapped", fmt.Errorf("rotation: %w", auth.ErrRefreshRevoked), codes.Unauthenticated},
		{"policy", auth.RejectRefresh(auth.RefreshRejectionIdleTimeout), codes.Unauthenticated},
		{"storage unavailable", errors.New("storage unavailable"), codes.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service = &business.Service{}
			service.SetJWTMinter(refreshOutcomeMinter{err: tc.err})
			_, err := (&AuthServer{}).RefreshToken(context.Background(), &gen.RefreshTokenRequest{RefreshToken: "test-refresh-token"})
			if status.Code(err) != tc.want {
				t.Fatalf("refresh status=%s; want %s", status.Code(err), tc.want)
			}
			if tc.want == codes.Unauthenticated && status.Convert(err).Message() != "session refresh rejected" {
				t.Fatal("refresh rejection must not distinguish token existence or rejection reason")
			}
		})
	}
}
