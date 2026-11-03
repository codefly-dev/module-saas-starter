package adapters

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/auth"
)

// deniedReader installs a collectable meter provider and rebinds the denial
// counter against it. The rebind is the point: rpcPolicyDeniedCounter binds
// once, lazily, and any earlier test in this package that ran an interceptor
// would otherwise have bound it to the no-op provider, leaving this case
// collecting an empty reader and passing for the wrong reason.
func deniedReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	denialCounterOnce = sync.Once{}
	denialCounter = nil
	t.Cleanup(func() {
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		denialCounterOnce = sync.Once{}
		denialCounter = nil
	})
	return reader
}

// deniedPoints reads the denial counter, keyed "method reason".
func deniedPoints(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	points := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "saas.accounts.rpc_policy.denied" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "the denial metric is an int64 sum")
			for _, point := range sum.DataPoints {
				method, hasMethod := point.Attributes.Value("method")
				reason, hasReason := point.Attributes.Value("reason")
				require.True(t, hasMethod, "a data point carries a method attribute")
				require.True(t, hasReason, "a data point carries a reason attribute")
				points[method.AsString()+" "+reason.AsString()] += point.Value
			}
		}
	}
	return points
}

const deniedMethod = "/saas.accounts.v1.APIKeyService/CreateAPIKey"

// A refusal is counted under the gate that produced it, labelled by method, in
// both error representations the two transports produce for the same gate.
func TestPolicyRefusalsAreCountedByMethodAndReason(t *testing.T) {
	reader := deniedReader(t)
	ctx := context.Background()

	grpcRefusal := status.Error(codes.PermissionDenied, "requires organization administrator or owner role")
	connectRefusal := connect.NewError(connect.CodePermissionDenied, errors.New("this operation does not accept API keys"))

	require.Equal(t, grpcRefusal, recordPolicyDenial(ctx, deniedMethod, denialReasonCentral, grpcRefusal))
	require.Equal(t, connectRefusal, recordPolicyDenial(ctx, deniedMethod, denialReasonAPIKeyScope, connectRefusal))
	require.Equal(t, grpcRefusal, recordPolicyDenial(ctx, deniedMethod, denialReasonHandler, grpcRefusal))

	points := deniedPoints(t, reader)
	require.Equal(t, int64(1), points[deniedMethod+" "+denialReasonCentral])
	require.Equal(t, int64(1), points[deniedMethod+" "+denialReasonAPIKeyScope])
	require.Equal(t, int64(1), points[deniedMethod+" "+denialReasonHandler])
}

// The central floor's no-verified-scope branch answers Unauthenticated, not
// PermissionDenied. It is a refusal of exactly the kind this metric exists to
// make alertable, so it is counted; a test that only asserted PermissionDenied
// would let that whole branch go dark.
func TestUnauthenticatedCentralRefusalIsCounted(t *testing.T) {
	reader := deniedReader(t)
	refusal := membershipLookupStatus("verify central org scope", auth.ErrVerifiedDatabaseIdentityRequired)
	require.Equal(t, codes.Unauthenticated, status.Code(refusal))
	recordPolicyDenial(context.Background(), deniedMethod, denialReasonCentral, refusal)
	require.Equal(t, int64(1), deniedPoints(t, reader)[deniedMethod+" "+denialReasonCentral])
}

// Nothing but a refusal is counted. The interceptor passes every handler error
// through recordPolicyDenial, so counting an Internal or an InvalidArgument
// would quietly turn an authorization metric into a general error rate — and an
// alert on it into noise nobody can act on.
func TestNonRefusalsAreNotCounted(t *testing.T) {
	reader := deniedReader(t)
	ctx := context.Background()
	for _, err := range []error{
		nil,
		status.Error(codes.Internal, "cannot verify membership"),
		status.Error(codes.InvalidArgument, "org_id required"),
		status.Error(codes.Unavailable, "authorization temporarily unavailable"),
		connect.NewError(connect.CodeInternal, errors.New("boom")),
		errors.New("an untyped error"),
	} {
		require.Equal(t, err, recordPolicyDenial(ctx, deniedMethod, denialReasonHandler, err))
	}
	require.Empty(t, deniedPoints(t, reader))
}
