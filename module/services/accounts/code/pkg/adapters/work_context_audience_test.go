package adapters

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every mint runs one audience rule, and it refuses BY NAME (issue #952).
//
// An audience is what makes two capabilities signed by one key
// non-interchangeable. An empty one was legal on every caller-supplied mint, and
// the verifier could not make up the difference: sdk-go's expectation check
// treats an empty EXPECTED audience as "do not check"
// (workcontext/work_context.go:570), so a verifier site with no value to pass and
// a token with no value to compare agreed vacuously. The host's half of the fix
// is to stamp a value always, which is what makes that sentinel unreachable from
// here.
func TestAMintAudienceIsRequiredAndRefusedByName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		audience string
		code     codes.Code
		says     string
	}{
		{"empty", "", codes.InvalidArgument, "audience is required"},
		{"blank", "   ", codes.InvalidArgument, "audience is required"},
		{"the capability surface itself", ModuleWorkContextAudience, codes.PermissionDenied, ModuleWorkContextAudience},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireMintableAudience(tc.audience)
			require.Error(t, err)
			require.Equal(t, tc.code, status.Code(err),
				"the two refusals are different facts and must not share a code")
			require.Contains(t, err.Error(), tc.says,
				"a refusal that does not name what it refused sends an operator to the wrong place")
		})
	}
}

// A whitespace-only audience is not a value. It is the shape a caller lands on by
// templating an empty variable, and treating it as present would stamp a token
// whose audience no consumer can match while reading as "has one".
func TestAnAudienceThatIsOnlyWhitespaceIsNotAnAudience(t *testing.T) {
	require.Error(t, requireMintableAudience("\t\n "))
	require.NoError(t, requireMintableAudience("example-consumer"),
		"an ordinary consumer audience must still pass, or the rule refuses everything")
}
