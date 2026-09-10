package adapters

import (
	"testing"

	"accounts/pkg/business"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A client has to be able to tell a refusal it can recover from by re-reading
// apart from one it must never retry. While these two shared a code, the
// auth-gateway could distinguish neither and so retried neither, and a
// registration change reaching a replica with a cold cache failed until that
// replica's next reconcile tick.
func TestSolutionRegistryErrorSeparatesRecoverableFromPermanent(t *testing.T) {
	recoverable := status.Code(solutionRegistryError(business.ErrSolutionRegistrationRevisionRequired))
	permanent := status.Code(solutionRegistryError(business.ErrSolutionRegistrationTombstoned))

	if recoverable != codes.Aborted {
		t.Fatalf("revision required = %v, want Aborted (re-read and retry)", recoverable)
	}
	if permanent != codes.FailedPrecondition {
		t.Fatalf("tombstoned = %v, want FailedPrecondition (never retry)", permanent)
	}
	if recoverable == permanent {
		t.Fatal("a recoverable refusal and a permanent one must not share a code")
	}
}
