package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	authorityv1 "github.com/codefly-dev/authority/libraries/authority-client/go/gen/host/authority/v1"
	"github.com/codefly-dev/authority/libraries/hop/go/routing"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The real front owns publication and renewal. The test authority controls only
// its acknowledgement; the gateway has no test-only readiness predicate.
type publicationAuthority struct {
	*hostAuthority
	allow atomic.Bool
	short bool
}

func (a *publicationAuthority) RegisterAdmissionKey(ctx context.Context, req *authorityv1.RegisterAdmissionKeyRequest, opts ...grpc.CallOption) (*authorityv1.RegisterAdmissionKeyResponse, error) {
	if !a.allow.Load() {
		return nil, status.Error(codes.Unavailable, "publication unavailable")
	}
	response, err := a.hostAuthority.RegisterAdmissionKey(ctx, req, opts...)
	if err == nil && a.short {
		response.ExpiresAtUnix = time.Now().Add(2 * time.Second).Unix()
	}
	return response, err
}

func readinessResponse(g *Gateway, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestGatewayReadinessUsesTheConfiguredFrontPublication(t *testing.T) {
	for _, scenario := range []string{"initial refusal and recovery", "expiry", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("CODEFLY__ENVIRONMENT", "local")
			gateway, _, _, _ := newGatewayHarness(t)
			require.Equal(t, http.StatusOK, readinessResponse(gateway, "/ready").Code, "no configured hop retains ordinary readiness")
			gateway.hop = &hopRole{}
			require.Equal(t, http.StatusServiceUnavailable, readinessResponse(gateway, "/ready").Code, "a configured role without its front refuses")
			gateway.hop = nil
			authority := &publicationAuthority{hostAuthority: &hostAuthority{t: t}, short: scenario == "expiry"}
			authority.allow.Store(scenario != "initial refusal and recovery")
			routes := &routing.Routing{Schema: routing.RoutingSchema, Endpoints: []routing.RouteEndpoint{{
				Audience: "example-module", Service: "api", Endpoint: "connect", Transport: "connect",
				Address: gateway.upstreams["accounts"].String(), Package: "example", Version: "0.1.0", Digest: "sha256:d",
				Methods: []string{listCollection},
			}}}
			raw, err := json.Marshal(routes)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			role, front, err := newHopRole(ctx, authority, workloadCredential, []string{"example-module"}, string(raw), routes, "")
			require.NoError(t, err)
			t.Cleanup(front.Close)
			gateway.hop = role

			switch scenario {
			case "initial refusal and recovery":
				require.False(t, front.Ready())
				refused := readinessResponse(gateway, "/ready")
				require.Equal(t, http.StatusServiceUnavailable, refused.Code)
				require.Contains(t, refused.Body.String(), "hop admission key not published")
				authority.allow.Store(true)
				require.Eventually(t, front.Ready, 8*time.Second, 10*time.Millisecond)
				require.Equal(t, http.StatusOK, readinessResponse(gateway, "/ready").Code)
				// Publication cannot substitute for the existing dependency checks.
				authz := gateway.authz
				gateway.authz = nil
				require.Equal(t, http.StatusServiceUnavailable, readinessResponse(gateway, "/ready").Code)
				gateway.authz = &ExtAuthz{}
				require.Contains(t, readinessResponse(gateway, "/ready").Body.String(), "access-token verification keys never loaded")
				gateway.authz = authz
				upstream := gateway.upstreams["accounts"]
				delete(gateway.upstreams, "accounts")
				require.Equal(t, http.StatusServiceUnavailable, readinessResponse(gateway, "/ready").Code)
				gateway.upstreams["accounts"] = upstream
				front.Close()
			case "expiry":
				require.True(t, front.Ready())
				require.Equal(t, http.StatusOK, readinessResponse(gateway, "/ready").Code)
				authority.allow.Store(false)
				require.Eventually(t, func() bool { return !front.Ready() }, 4*time.Second, 10*time.Millisecond)
			case "cancellation":
				require.True(t, front.Ready())
				cancel()
			}
			require.False(t, front.Ready())
			require.Equal(t, http.StatusServiceUnavailable, readinessResponse(gateway, "/ready").Code)
			require.Equal(t, http.StatusOK, readinessResponse(gateway, "/health").Code, "publication loss is not process death")
		})
	}
}
