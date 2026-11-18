package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
)

// A page token a listing did not issue is the caller's mistake, answered as
// InvalidArgument with the store's sentence, also through the wrapping the
// business layer adds on the way up. Any other failure is passed on unchanged.
func TestAPageTokenTheListingDidNotIssueIsAnInvalidArgument(t *testing.T) {
	refused := business.NewStoreError(errors.New("page_token is not one this listing issued"), business.ErrTypeValidation)
	wrapped := wool.Get(context.Background()).In("ListUsers").Wrapf(refused, "cannot list users")
	for name, err := range map[string]error{"as the store returns it": refused, "wrapped by the business layer": wrapped} {
		t.Run(name, func(t *testing.T) {
			got := pageTokenStatus(err)
			require.Equal(t, codes.InvalidArgument, status.Code(got))
			require.Equal(t, "page_token is not one this listing issued", status.Convert(got).Message())
		})
	}
	other := errors.New("connection reset")
	require.Same(t, other, pageTokenStatus(other))
	require.NoError(t, pageTokenStatus(nil))
}
