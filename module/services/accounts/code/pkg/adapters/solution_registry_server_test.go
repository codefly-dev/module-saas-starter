package adapters

import (
	"fmt"
	"strings"
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

// A refused audit event declaration is permanent for that manifest: the
// registrant must change what it declares, so it is InvalidArgument — which the
// gateway relays as 400 — never a code that invites a retry or reads as a
// publisher conflict. The message says which rule the declaration broke.
func TestSolutionRegistryErrorRejectsAuditDeclarationAsInvalid(t *testing.T) {
	_, err := business.ParseDeclaredAuditEventTypes("acme",
		`{"dashboard":{"events":[{"name":"e","type":"saas.item.created","fields":[]}]}}`)
	mapped := solutionRegistryError(err)
	if status.Code(mapped) != codes.InvalidArgument {
		t.Fatalf("rejected declaration = %v, want InvalidArgument", status.Code(mapped))
	}
	if msg := status.Convert(mapped).Message(); !strings.Contains(msg, "reserved namespace") {
		t.Fatalf("message %q does not name the broken rule", msg)
	}
	owned := fmt.Errorf("%w: %w: namespace held", business.ErrSolutionAuditDeclarationRejected, business.ErrSolutionAuditNamespaceOwned)
	if status.Code(solutionRegistryError(owned)) != codes.InvalidArgument {
		t.Fatal("a namespace held by another producer must be InvalidArgument too")
	}
}
