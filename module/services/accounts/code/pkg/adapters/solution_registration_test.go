package adapters

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordingSolutionMinter struct {
	solutionID string
}

func (m *recordingSolutionMinter) MintSolutionRegistration(solutionID string) (string, time.Time, error) {
	m.solutionID = solutionID
	return "signed-token-for-" + solutionID, time.Unix(1700000000, 0).UTC(), nil
}

// installSolutionRegistrar wires a real business.Service against a declaration
// the test can rewrite afterwards, which is how a deployment authorizes a
// solution against a host that is already serving.
func installSolutionRegistrar(t *testing.T, declared *string) *recordingSolutionMinter {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })

	svc, err := business.NewService(&moduleRegistrationStore{})
	require.NoError(t, err)
	minter := &recordingSolutionMinter{}
	svc.SetSolutionRegistrar(minter, func() string { return *declared })
	WithService(svc)
	return minter
}

func mintSolution(t *testing.T, id, secret string) (*gen.SolutionMintRegistrationResponse, error) {
	t.Helper()
	return ModuleCapabilitiesSingleton().MintSolutionRegistration(context.Background(),
		&gen.SolutionMintRegistrationRequest{SolutionId: id, Secret: secret})
}

func TestMintSolutionRegistrationIssuesForDeclaredSolution(t *testing.T) {
	declared := "solution-a:" + registrationDigest("solution-a-secret")
	minter := installSolutionRegistrar(t, &declared)

	resp, err := mintSolution(t, "solution-a", "solution-a-secret")

	require.NoError(t, err)
	require.Equal(t, "signed-token-for-solution-a", resp.GetToken())
	require.Equal(t, "solution-a", minter.solutionID)
	require.Equal(t, int64(1700000000), resp.GetExpiresAt().AsTime().Unix())
}

// The invariant the registration seam exists to provide: a solution is added to
// a host that is already serving. Authorizing one must take effect when it is
// authorized, not at the next restart of this service — which is what holding
// the parsed allowlist for the process lifetime cost.
func TestAuthorizingASolutionTakesEffectWithoutRestart(t *testing.T) {
	declared := "solution-a:" + registrationDigest("solution-a-secret")
	minter := installSolutionRegistrar(t, &declared)

	_, err := mintSolution(t, "solution-b", "solution-b-secret")
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	declared += ",solution-b:" + registrationDigest("solution-b-secret")

	resp, err := mintSolution(t, "solution-b", "solution-b-secret")
	require.NoError(t, err)
	require.Equal(t, "signed-token-for-solution-b", resp.GetToken())
	require.Equal(t, "solution-b", minter.solutionID)

	// The solution that was already serving keeps its credential.
	_, err = mintSolution(t, "solution-a", "solution-a-secret")
	require.NoError(t, err)
}

// The same property in the direction that matters for revocation: withdrawing a
// publisher must stop it renewing, rather than leaving it authorized until this
// service happens to restart.
func TestWithdrawingASolutionTakesEffectWithoutRestart(t *testing.T) {
	declared := "solution-a:" + registrationDigest("solution-a-secret") + ",solution-b:" + registrationDigest("solution-b-secret")
	installSolutionRegistrar(t, &declared)

	_, err := mintSolution(t, "solution-b", "solution-b-secret")
	require.NoError(t, err)

	declared = "solution-a:" + registrationDigest("solution-a-secret")

	_, err = mintSolution(t, "solution-b", "solution-b-secret")
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = mintSolution(t, "solution-a", "solution-a-secret")
	require.NoError(t, err)
}

// Re-reading the declaration must not loosen the per-solution binding: holding
// one publisher's secret can never obtain another's credential.
func TestMintSolutionRegistrationBindsSecretToSolution(t *testing.T) {
	declared := "solution-a:" + registrationDigest("solution-a-secret") + ",solution-b:" + registrationDigest("solution-b-secret")
	minter := installSolutionRegistrar(t, &declared)

	_, err := mintSolution(t, "solution-b", "solution-a-secret")

	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, minter.solutionID)
}

func TestMintSolutionRegistrationFailsClosed(t *testing.T) {
	tests := map[string]struct {
		declared string
		id       string
		secret   string
	}{
		"wrong secret":        {"solution-a:" + registrationDigest("right"), "solution-a", "guessed"},
		"undeclared solution": {"solution-a:" + registrationDigest("right"), "solution-b", "right"},
		"nothing declared":    {"", "solution-a", "right"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			declared := test.declared
			minter := installSolutionRegistrar(t, &declared)

			_, err := mintSolution(t, test.id, test.secret)

			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Empty(t, minter.solutionID)
		})
	}
}

// A declaration edited into an unparseable state after boot must deny every
// solution — including one that was authorized a moment earlier — rather than
// read as an empty allowlist or surface as an internal error.
func TestMintSolutionRegistrationDeniesOnMalformedDeclaration(t *testing.T) {
	declared := "solution-a:" + registrationDigest("solution-a-secret")
	minter := installSolutionRegistrar(t, &declared)

	_, err := mintSolution(t, "solution-a", "solution-a-secret")
	require.NoError(t, err)
	minter.solutionID = ""

	declared = "solution-a:not-a-digest"

	_, err = mintSolution(t, "solution-a", "solution-a-secret")
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, minter.solutionID)
}

// An unset registrar denies rather than treating "no policy" as "any policy".
func TestMintSolutionRegistrationDeniesWithoutRegistrar(t *testing.T) {
	previous := service
	t.Cleanup(func() { service = previous })
	svc, err := business.NewService(&moduleRegistrationStore{})
	require.NoError(t, err)
	WithService(svc)

	_, err = mintSolution(t, "solution-a", "solution-a-secret")

	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
