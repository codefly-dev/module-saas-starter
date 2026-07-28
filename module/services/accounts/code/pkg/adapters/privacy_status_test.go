package adapters

import (
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnavailablePrivacyWorkflowIsAFailedPrecondition(t *testing.T) {
	err := privacyStatusError(business.ErrPrivacyWorkflowUnavailable)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
