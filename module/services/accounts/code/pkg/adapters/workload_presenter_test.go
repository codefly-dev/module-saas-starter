package adapters

import (
	"context"
	"testing"

	"accounts/pkg/business"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestWorkloadPresenterRequiresGatewayProvenanceBeforeInternalAdmission(t *testing.T) {
	previousGateway, previousInternal := gatewayToken, internalToken
	SetGatewayToken("gateway-test")
	SetInternalToken("internal-test")
	t.Cleanup(func() { SetGatewayToken(previousGateway); SetInternalToken(previousInternal) })
	const principal = "spiffe://cluster.local/ns/modules/sa/worker"
	for _, exposure := range []rpcExposure{rpcExposureInternal, rpcExposureModule} {
		for _, tc := range []struct {
			name                string
			gateway, presenters []string
			want                string
			refused             bool
		}{
			{name: "verified", gateway: []string{"gateway-test"}, presenters: []string{principal}, want: principal},
			{name: "untrusted", gateway: []string{"wrong"}, presenters: []string{principal}},
			{name: "no gateway", presenters: []string{principal}},
			{name: "duplicate", gateway: []string{"gateway-test"}, presenters: []string{principal, principal}, refused: true},
			{name: "malformed", gateway: []string{"gateway-test"}, presenters: []string{principal + "?"}, refused: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				md := metadata.Pairs("x-codefly-internal-token", "internal-test")
				md["x-codefly-gateway-token"], md["x-codefly-workload-presenter"] = tc.gateway, tc.presenters
				ctx, err := (&grpcPolicyAuthorizer{exposure: exposure}).authorize(metadata.NewIncomingContext(context.Background(), md), "/saas.accounts.v1.ModuleCapabilitiesService/MintModuleWorkContext")
				// The module-authority listener must never expose gateway-only mints.
				if tc.refused || exposure == rpcExposureModule {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tc.want, business.VerifiedWorkloadPresenter(ctx))
				incoming, _ := metadata.FromIncomingContext(ctx)
				require.Empty(t, incoming.Get("x-codefly-workload-presenter"))
				require.Empty(t, incoming.Get("x-codefly-gateway-token"))
			})
		}
	}
}
