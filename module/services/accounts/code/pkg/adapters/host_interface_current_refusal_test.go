package adapters

import (
	"context"
	"fmt"
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Current refuses a credential that proves no module as PermissionDenied, with
// the reason, and answered every other workload error the same way: a
// credential read the request's end cut short was refused as one that proves
// no module. It now keeps the failure's code.
func TestCurrentRefusesOnlyACredentialThatProvesNoModule(t *testing.T) {
	ended, end := context.WithCancel(context.Background())
	end()
	for name, tc := range map[string]struct {
		err  error
		code codes.Code
	}{
		"request ended":    {credentialRefusal(ended, fmt.Errorf("%w: %w", business.ErrExecutionUnbound, context.Canceled), "the actor", ""), codes.Canceled},
		"deadline passed":  {credentialRefusal(context.Background(), fmt.Errorf("%w: %w", business.ErrExecutionUnbound, context.DeadlineExceeded), "the actor", ""), codes.DeadlineExceeded},
		"review unbound":   {credentialRefusal(context.Background(), fmt.Errorf("%w: the api server did not answer", business.ErrExecutionUnbound), "the actor", ""), codes.Unavailable},
		"registry unread":  {status.Error(codes.Unavailable, "the module registry could not be read current"), codes.Unavailable},
		"unknown failure":  {credentialRefusal(context.Background(), fmt.Errorf("the reviewer panicked"), "the actor", ""), codes.Internal},
		"review refused":   {credentialRefusal(context.Background(), fmt.Errorf("%w: expired", business.ErrExecutionTokenRejected), "the actor", ""), codes.PermissionDenied},
		"identity refused": {credentialRefusal(context.Background(), fmt.Errorf("%w: moved tenant", business.ErrModuleIdentityDenied), "the actor", ""), codes.PermissionDenied},
		"no credential":    {credentialRefusal(context.Background(), fmt.Errorf("%w: none", business.ErrExecutionNotBoundToken), "the actor", ""), codes.PermissionDenied},
	} {
		t.Run(name, func(t *testing.T) {
			err := refusedUnlessFailed(tc.err)
			require.Equal(t, tc.code, status.Code(err), "%v", err)
			require.Equal(t, status.Convert(tc.err).Message(), status.Convert(err).Message(), "the reason travels unchanged")
		})
	}
}
