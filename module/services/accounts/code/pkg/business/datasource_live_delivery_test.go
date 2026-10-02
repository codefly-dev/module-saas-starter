package business_test

// How a change at a source reaches this deployment — and why the flag that was
// there before could not say.
//
// webhook_configured reports one thing: whether a signing secret is stored for
// the source. A source connected through the GitHub App, the recommended path,
// holds no secret of its own; its pushes arrive at the App's single webhook URL.
// So every App-backed source read as "Not configured" whether or not the
// operator had registered that webhook — which is exactly why "is the webhook
// configured on this environment?" could not be answered from the product. The
// converse is just as wrong: a stored per-source secret delivers nothing on a
// deployment that never mounted the receiver, which is off by default.

import (
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestLiveDeliveryFor(t *testing.T) {
	const appKey = `-----BEGIN RSA PRIVATE KEY-----
not-a-real-key
-----END RSA PRIVATE KEY-----`

	appSource := &business.DatasourceSource{
		Provider: business.DatasourceProviderGitHub, GitHubInstallationID: "42",
	}
	patSource := &business.DatasourceSource{
		Provider: business.DatasourceProviderGitHub, WebhookSecretRef: "envelope",
	}
	bareSource := &business.DatasourceSource{Provider: business.DatasourceProviderGitHub}
	pullSource := &business.DatasourceSource{
		Provider: business.DatasourceProviderCrawler, WebhookSecretRef: "envelope",
	}

	for _, tc := range []struct {
		name       string
		appWebhook bool
		mounted    bool
		source     *business.DatasourceSource
		want       business.DatasourceLiveDelivery
	}{
		{"app-backed source on a deployment with the App webhook", true, false, appSource, business.DatasourceLiveDeliveryAppWebhook},
		{"app-backed source on a deployment without it", false, false, appSource, business.DatasourceLiveDeliveryNone},
		{"own secret, receiver mounted", false, true, patSource, business.DatasourceLiveDeliverySourceWebhook},
		{"own secret, receiver not mounted", true, false, patSource, business.DatasourceLiveDeliveryNone},
		{"no secret and no installation", true, true, bareSource, business.DatasourceLiveDeliveryNone},
		{"a pull provider has no push path at all", true, true, pullSource, business.DatasourceLiveDeliveryNone},
		{"no source", true, true, nil, business.DatasourceLiveDeliveryNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := business.NewService(newDatasourceFakeStore())
			require.NoError(t, err)
			if tc.appWebhook {
				svc.SetGitHubAppRegistration("123", appKey, "acme-app", "secret")
			}
			svc.SetDatasourceWebhookReceiverMounted(tc.mounted)
			require.Equal(t, tc.want, svc.LiveDeliveryFor(tc.source))
		})
	}
}

// The catalog's deployment-level answer: whether an operator has wired this
// connector's push endpoint here, beside the descriptor's "the connector could
// take one at all". Without the pair, "this provider has no push path" and "it
// has one and nobody turned it on" are indistinguishable from every client.
func TestDatasourceLiveDeliveryConfigured(t *testing.T) {
	svc, err := business.NewService(newDatasourceFakeStore())
	require.NoError(t, err)

	require.False(t, svc.DatasourceLiveDeliveryConfigured(business.DatasourceProviderGitHub))
	require.False(t, svc.DatasourceLiveDeliveryConfigured(business.DatasourceProviderCrawler))

	svc.SetDatasourceWebhookReceiverMounted(true)
	require.True(t, svc.DatasourceLiveDeliveryConfigured(business.DatasourceProviderGitHub),
		"the per-source receiver being mounted is a push endpoint an operator wired")
	require.False(t, svc.DatasourceLiveDeliveryConfigured(business.DatasourceProviderCrawler),
		"a connector with no receiver in this host has nothing to configure")
}
