package adapters

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func collectCoverage(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))

	byCoverage := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "saas.accounts.rpc_policy.coverage" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "coverage metric is an int64 sum")
			for _, point := range sum.DataPoints {
				coverage, ok := point.Attributes.Value("coverage")
				require.True(t, ok, "data point carries a coverage attribute")
				byCoverage[coverage.AsString()] += point.Value
			}
		}
	}
	return byCoverage
}

func installMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	// The coverage counter binds to the provider live when it is first created;
	// rebind it so each test records into its own reader rather than the first.
	coverageCounterOnce = sync.Once{}
	coverageCounter = nil
	t.Cleanup(func() { otel.SetMeterProvider(metricnoop.NewMeterProvider()) })
	return reader
}

// ownedResourceShadowStore resolves the org for one webhook subscription so the
// shadow path can exercise the ownership resolver end to end.
type ownedResourceShadowStore struct {
	business.Store
	subscriptions map[string]*business.WebhookSubscription
}

func (f *ownedResourceShadowStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *ownedResourceShadowStore) GetWebhookSubscription(_ context.Context, id string) (*business.WebhookSubscription, error) {
	return f.subscriptions[id], nil
}

// The shadow signal must record a data point for every admitted call,
// including fully covered (ok) ones. Without the ok heartbeat, an absence of
// gap/unsupported points is indistinguishable from a dark instrument, and the
// "zero gap over real traffic" precondition for enforcement can read a false
// green.
func TestShadowPolicyCoverageRecordsEveryOutcomeIncludingOK(t *testing.T) {
	reader := installMeter(t)

	// GetUser is a covered self-service read (ok); CreateAPIKey requires an org
	// admin the interceptor does not yet resolve (gap). Neither binds an owned
	// resource, so the request argument is unused.
	shadowPolicyCoverage(context.Background(), "/saas.accounts.v1.UserService/GetUser", nil)
	shadowPolicyCoverage(context.Background(), "/saas.accounts.v1.APIKeyService/CreateAPIKey", nil)

	byCoverage := collectCoverage(t, reader)
	require.Equal(t, int64(1), byCoverage["ok"], "the ok heartbeat must be recorded")
	require.Equal(t, int64(1), byCoverage["gap"], "the gap signal must be recorded")
}

// When the ownership resolver maps the request's resource to an org, an
// owned-resource method records its static coverage (gap) — never unsupported.
func TestShadowPolicyCoverageResolvesOwnedResource(t *testing.T) {
	reader := installMeter(t)
	previous := service
	svc, err := business.NewService(&ownedResourceShadowStore{
		subscriptions: map[string]*business.WebhookSubscription{
			"sub-1": {ID: "sub-1", OrgID: "org-1"},
		},
	})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })

	shadowPolicyCoverage(context.Background(),
		"/saas.accounts.v1.WebhookService/DeleteSubscription",
		&gen.DeleteWebhookSubscriptionRequest{Id: "sub-1"})

	byCoverage := collectCoverage(t, reader)
	require.Equal(t, int64(1), byCoverage["gap"], "a resolved owned resource records its static gap")
	require.Zero(t, byCoverage["unsupported"], "a resolved owned resource is not unsupported")
}

// A resource id that does not resolve to an org fails closed: the shadow signal
// is downgraded to unsupported even though static classification is gap.
func TestShadowPolicyCoverageUnresolvedOwnedResourceIsUnsupported(t *testing.T) {
	reader := installMeter(t)
	previous := service
	svc, err := business.NewService(&ownedResourceShadowStore{
		subscriptions: map[string]*business.WebhookSubscription{},
	})
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })

	shadowPolicyCoverage(context.Background(),
		"/saas.accounts.v1.WebhookService/DeleteSubscription",
		&gen.DeleteWebhookSubscriptionRequest{Id: "missing"})

	byCoverage := collectCoverage(t, reader)
	require.Equal(t, int64(1), byCoverage["unsupported"], "an unresolved owned resource records unsupported")
	require.Zero(t, byCoverage["gap"], "an unresolved owned resource is not gap")
}
