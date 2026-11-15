package adapters

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A workload credential that could not be read is refused as the failure it
// was: the request ended (Canceled), its deadline passed (DeadlineExceeded),
// the review could not run (Unavailable), or anything else (Internal). Never
// PermissionDenied, which a caller reads as "this credential proves no
// module" and stops. A credential the review refused stays PermissionDenied,
// with its reason.
func TestCredentialRefusalTellsAFailureFromARefusal(t *testing.T) {
	ended, end := context.WithCancel(context.Background())
	end()
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		code codes.Code
	}{
		// Whether the caller ended its request is its context's to say; a
		// dependency answering Canceled while the caller is live could not
		// run the review.
		"request ended":            {ended, fmt.Errorf("%w: post tokenreviews: %w", business.ErrExecutionUnbound, context.Canceled), codes.Canceled},
		"request ended, any error": {ended, errors.New("the reviewer saw nothing"), codes.Canceled},
		"dependency cancelled":     {nil, fmt.Errorf("%w: post tokenreviews: %w", business.ErrExecutionUnbound, context.Canceled), codes.Unavailable},
		// A verdict is a refusal whatever became of the caller.
		"rejection, request ended":     {ended, fmt.Errorf("%w: the token has expired", business.ErrExecutionTokenRejected), codes.PermissionDenied},
		"no credential, request ended": {ended, fmt.Errorf("%w: no credential presented", business.ErrExecutionNotBoundToken), codes.Unauthenticated},
		"bare cancellation":            {nil, context.Canceled, codes.Unavailable},
		"deadline passed":              {nil, fmt.Errorf("%w: post tokenreviews: %w", business.ErrExecutionUnbound, context.DeadlineExceeded), codes.DeadlineExceeded},
		"review unbound":               {nil, fmt.Errorf("%w: the api server did not answer", business.ErrExecutionUnbound), codes.Unavailable},
		"unknown failure":              {nil, errors.New("the reviewer panicked"), codes.Internal},
		"review refused":               {nil, fmt.Errorf("%w: the token has expired", business.ErrExecutionTokenRejected), codes.PermissionDenied},
		"no credential":                {nil, fmt.Errorf("%w: no credential presented", business.ErrExecutionNotBoundToken), codes.Unauthenticated},
		"ambiguous":                    {nil, fmt.Errorf("%w: example/api", business.ErrExecutionWorkloadAmbiguous), codes.PermissionDenied},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			err := credentialRefusal(ctx, tc.err, "the actor", "")
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			require.Contains(t, status.Convert(err).Message(), "the actor credential", "every refusal names what it is about")
		})
	}
}

// End to end on a method that takes a workload credential: an API server that
// did not answer the review is Unavailable, where it was PermissionDenied.
func TestAnAdmissionKeyWhoseCredentialCouldNotBeReviewedIsUnavailable(t *testing.T) {
	f := newAdmissionFixture(t)
	f.cluster.Unreachable = true
	_, public := hopPublicKey(t, "the-hop")
	_, err := f.register(t, f.hop, public)
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)

	f.cluster.Unreachable = false
	_, err = f.register(t, "a-credential-no-cluster-issued", public)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "a refused credential stays refused: %v", err)
}

// A credential that could not be read is answered with what failed and the
// code, never the reviewer's own error: that can carry the API server's
// address or a transport error, and an in-mesh caller may relay it. The error
// goes to accounts' log instead.
func TestACredentialReadFailureKeepsTheReviewersErrorInTheLog(t *testing.T) {
	f := newAdmissionFixture(t)
	f.cluster.Unreachable = true
	logs := captureAdapterLogs(t)
	_, public := hopPublicKey(t, "the-hop")
	_, err := f.register(t, f.hop, public)
	require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
	require.Contains(t, status.Convert(err).Message(), "actor credential")
	require.NotContains(t, status.Convert(err).Message(), "the api server did not answer", "the reviewer's error stays in accounts")
	require.Contains(t, logs(), "the api server did not answer")
}
