package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// Tenant-isolation attack battery, accounts half. Each test replays one attack
// from the tenant-isolation audit and asserts the secure outcome; each was red
// before the fix it names.

// TestAttack_RESTHopCarriesOneIdentity: grpc-gateway's default header matcher
// turns any `Grpc-Metadata-<name>` header into `<name>` metadata. A signed-in
// caller who adds `Grpc-Metadata-X-User-Id: <victim>` to a request the gateway
// forwards (the gateway strips `X-User-Id`, not this spelling) reaches the REST
// hop carrying two x-user-id values: the one the gateway stamped and the one
// the caller chose. The Connect listener trusts the forwarded identity because
// the gateway token is present, and reads the first value, so the forgery wins
// whenever the caller's copy is ordered first.
func TestAttack_RESTHopCarriesOneIdentity(t *testing.T) {
	mux := runtime.NewServeMux(
		runtime.WithMetadata(CustomHeaderToGRPCMetadataAnnotator),
		runtime.WithIncomingHeaderMatcher(restIdentityHeaderMatcher),
	)
	const stamped, forged = "019fec91-1000-7000-8000-00000000aaaa", "019fec91-1000-7000-8000-00000000bbbb"
	for _, header := range forwardedIdentityHeaders {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/users/self", nil)
			req.Header.Set(header, stamped)
			req.Header.Set("Grpc-Metadata-"+header, forged)
			req.Header.Set("X-Codefly-Gateway-Token", "gateway-token")

			ctx, err := runtime.AnnotateContext(context.Background(), mux, req, "/saas.accounts.v1.UserService/GetSelf")
			require.NoError(t, err)
			md, _ := metadata.FromOutgoingContext(ctx)
			for _, value := range md.Get(header) {
				require.NotEqualf(t, forged, value,
					"a caller-supplied Grpc-Metadata-%s reached the identity metadata the Connect listener trusts", header)
			}
		})
	}
}
