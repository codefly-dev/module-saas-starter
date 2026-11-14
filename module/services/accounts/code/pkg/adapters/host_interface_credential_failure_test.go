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
	for name, tc := range map[string]struct {
		err  error
		code codes.Code
	}{
		"request ended":     {fmt.Errorf("%w: post tokenreviews: %w", business.ErrExecutionUnbound, context.Canceled), codes.Canceled},
		"deadline passed":   {fmt.Errorf("%w: post tokenreviews: %w", business.ErrExecutionUnbound, context.DeadlineExceeded), codes.DeadlineExceeded},
		"bare cancellation": {context.Canceled, codes.Canceled},
		"review unbound":    {fmt.Errorf("%w: the api server did not answer", business.ErrExecutionUnbound), codes.Unavailable},
		"unknown failure":   {errors.New("the reviewer panicked"), codes.Internal},
		"review refused":    {fmt.Errorf("%w: the token has expired", business.ErrExecutionTokenRejected), codes.PermissionDenied},
		"no credential":     {fmt.Errorf("%w: no credential presented", business.ErrExecutionNotBoundToken), codes.Unauthenticated},
		"ambiguous":         {fmt.Errorf("%w: example/api", business.ErrExecutionWorkloadAmbiguous), codes.PermissionDenied},
	} {
		t.Run(name, func(t *testing.T) {
			err := credentialRefusal(tc.err, "the actor")
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
