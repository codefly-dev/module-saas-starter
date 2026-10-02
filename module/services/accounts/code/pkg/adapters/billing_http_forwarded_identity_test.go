package adapters

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
)

// authenticateHTTPRequest, the transport behind billing and the subscription
// stream, applies the same trust test as Connect and gRPC: exactly one gateway
// credential, and a trusted assertion that names an identity field twice is
// refused rather than read by index.
func TestBillingHTTPForwardedIdentityMatchesTheOtherTransports(t *testing.T) {
	previousGateway := gatewayToken
	SetGatewayToken("test-gateway-token")
	t.Cleanup(func() { SetGatewayToken(previousGateway) })

	user := uuid.Must(uuid.NewV7()).String()
	org := uuid.Must(uuid.NewV7()).String()
	other := uuid.Must(uuid.NewV7()).String()
	admit := forwardedTransports()["billing http"]

	t.Run("two gateway credentials are not trusted", func(t *testing.T) {
		_, err := admit(t, http.Header{
			"X-Codefly-Gateway-Token": {"test-gateway-token", "caller-chosen"},
			"X-User-Id":               {user},
			"X-Org-Id":                {org},
		})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "ambiguous", "the forwarded identity is ignored, not judged")
	})

	for _, name := range append([]string{publicOriginHeader}, forwardedIdentityHeaders...) {
		t.Run("two values of "+name, func(t *testing.T) {
			headers := http.Header{
				"X-Codefly-Gateway-Token": {"test-gateway-token"},
				"X-Credential-Kind":       {credentialKindSession},
				"X-Scopes":                {""},
				"X-User-Id":               {user},
				"X-Org-Id":                {org},
			}
			headers[http.CanonicalHeaderKey(name)] = []string{other, user}
			_, err := admit(t, headers)
			require.Error(t, err)
			require.Contains(t, err.Error(), "ambiguous")
		})
	}

	t.Run("one credential and one value per field", func(t *testing.T) {
		_, userID, orgID, err := authenticateHTTPRequest(&business.Service{}, &http.Request{Header: http.Header{
			"X-Codefly-Gateway-Token": {"test-gateway-token"},
			"X-Credential-Kind":       {credentialKindSession},
			"X-Scopes":                {""},
			"X-User-Id":               {user},
			"X-Org-Id":                {org},
		}})
		require.NoError(t, err)
		require.Equal(t, user, userID)
		require.Equal(t, org, orgID)
	})
}
