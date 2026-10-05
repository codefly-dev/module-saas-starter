package business

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// recordingValidator stands in for the provider stack a deployment builds. The
// assertion that matters is whether it is CONSULTED: an id_token presented in the
// request body must never reach the code-exchange validator, which checks a
// signature, an issuer, an audience and an expiry — and no nonce, because the
// nonce is the code exchange's binding to a single authorize request and lives in
// the state that exchange holds.
type recordingValidator struct{ consulted bool }

func (v *recordingValidator) Validate(context.Context, string) (*auth.Claims, error) {
	v.consulted = true
	return &auth.Claims{Subject: "subject", Email: "person@example.com", Provider: "oidc"}, nil
}

type refusingResolver struct{}

func (refusingResolver) Resolve(context.Context, *auth.Claims, auth.Intent) (*auth.Identity, error) {
	return nil, errors.New("the resolver must not be reached")
}

type headerJWTModeStore struct{ Store }

// The only store read either test reaches. Both end before any identity exists:
// what each asserts is whether the VALIDATOR was consulted.
func (*headerJWTModeStore) ResolveIdentity(context.Context, string, string) (*ResolvedIdentity, error) {
	return nil, errors.New("no identity in this fixture")
}

// In OIDC mode the header-jwt login path is inactive, and a body carrying an
// identity assertion is refused rather than validated. It used to be handed to
// the OIDC validator, which admitted any unexpired id_token for the configured
// client with no state, no proof-of-possession verifier and no nonce — a full
// session from an assertion that completed none of the handoff.
func TestL1VHeaderJWTBodyRefusedInOIDCMode(t *testing.T) {
	validator := &recordingValidator{}
	service, err := NewService(&headerJWTModeStore{})
	require.NoError(t, err)
	service.SetIdentityResolver(refusingResolver{})
	service.SetJWTMinter(stubMinterForHeaderJWTMode{})
	// Exactly what work.go's OIDC branch wires: the code-exchange validator, and
	// no header-jwt validator.
	service.SetTokenValidator(validator)

	resp, err := service.Authenticate(context.Background(), &gen.AuthenticateRequest{
		Provider: "oidc",
		Authentication: &gen.AuthenticateRequest_HeaderJwt{
			HeaderJwt: &gen.HeaderJWTAuthentication{Token: "an.unexpired.idtoken"},
		},
	})

	require.Nil(t, resp)
	require.ErrorIs(t, err, auth.ErrInvalidOAuthRequest)
	require.False(t, validator.consulted,
		"the body credential must be refused before any validator sees it")
}

// The counterpart: with the header-jwt validator wired — the one thing work.go's
// header-jwt branch does and no other branch does — the credential is accepted
// and reaches that validator, so the refusal above is the mode and not a path
// that refuses everything.
func TestHeaderJWTBodyIsValidatedInHeaderJWTMode(t *testing.T) {
	headerJWT := &recordingValidator{}
	service, err := NewService(&headerJWTModeStore{})
	require.NoError(t, err)
	service.SetIdentityResolver(refusingResolver{})
	service.SetJWTMinter(stubMinterForHeaderJWTMode{})
	service.SetHeaderJWTTokenValidator(headerJWT)

	_, err = service.Authenticate(context.Background(), &gen.AuthenticateRequest{
		Provider: "header-jwt",
		Authentication: &gen.AuthenticateRequest_HeaderJwt{
			HeaderJwt: &gen.HeaderJWTAuthentication{Token: "an.unexpired.idtoken"},
		},
	})

	require.True(t, headerJWT.consulted, "the header-jwt validator must see the credential")
	require.Error(t, err, "the refusing resolver is what ends this call")
}

type stubMinterForHeaderJWTMode struct{ auth.JWTMinter }
