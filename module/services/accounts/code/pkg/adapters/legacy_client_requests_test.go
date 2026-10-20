package adapters

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// THE OLDEST SUPPORTED CLIENT STILL GETS AN ANSWER.
//
// Adding a field to a request message is backward compatible; REQUIRING it is
// not, and the two look identical in a diff. A consumer generated before the
// field exists simply does not set it, so the moment a handler refuses that
// shape as malformed, every unported consumer breaks at once — and breaks
// badly, because InvalidArgument is not a denial: a caller that maps only
// PermissionDenied / FailedPrecondition / NotFound to "denied" reports
// everything else as "the host could not be consulted", i.e. an outage.
//
// That is not hypothetical. A change to CheckAuthorizationRevision proposed
// refusing a request that states neither operation_binding_id nor
// seals_no_binding. robin's chat builds exactly that request
// (modules/robin/services/chat/code/internal/authority/replay_host.go: OrgId,
// OwnerPrincipalId, AuthorizationRevision and Subjects, and neither binding
// field) and maps anything outside those three codes to ErrUnavailable. Every
// module hop verification on a cell would have failed, reported as an
// authority outage, with no statement of the cause anywhere.
//
// So a new requirement is a DECLARED CUTOVER, in two phases: ship the field and
// port every consumer first, while the handler still answers the old shape;
// then make it mandatory, in a change that names the release each consumer was
// ported in. This test is phase one's guard rail. It holds the legacy request
// for each RPC that has gained a field, and fails the moment a handler refuses
// that shape.
//
// If you are here because this test failed: you are making phase two. Confirm
// every consumer sets the field, name the release that ported each one in the
// pull request, and only then change the request this test builds.
func TestTheOldestSupportedClientIsStillAnswered(t *testing.T) {
	installModuleOperationContextService(t, headlessPrincipals)
	installModuleWorkContextAuthority(t)
	ctx := revisionTestContext(t)

	signed := mintedOperationContextClaims(t)
	legacy := revisionRequestFromClaims(signed)
	// A consumer generated before the binding fields existed sets neither,
	// whatever the context it holds actually sealed.
	legacy.OperationBindingId = nil
	require.NoError(t, Validate(legacy))

	_, err := WorkContextSingleton().CheckAuthorizationRevision(ctx, legacy)
	require.NotEqual(t, codes.InvalidArgument, status.Code(err),
		"a request from a consumer that predates the binding fields must be judged, not refused as malformed: "+
			"InvalidArgument reaches robin's chat as an authority outage, not a denial. Read this test's comment.")
}
