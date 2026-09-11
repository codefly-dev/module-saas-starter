package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	moduleWorkContextTenant = "55555555-5555-4555-8555-555555555555"
	documentsPrincipals     = `{"documents":{"queues":["datasource"],"namespaces":["document"],"tenant":"` +
		moduleWorkContextTenant + `"}}`
)

// recordingModuleStore observes the control-plane transaction the issuance
// record is written in, so a test can tell whether the record was attempted at
// all and force it to fail.
type recordingModuleStore struct {
	business.Store
	controlPlaneCalls int
	controlPlaneErr   error
}

func (s *recordingModuleStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	s.controlPlaneCalls++
	if s.controlPlaneErr != nil {
		return s.controlPlaneErr
	}
	return fn(ctx)
}

// installModuleWorkContextAuthority configures the shared issuer with a
// throwaway key, so a mint and the verification that consumes it run through the
// real signer rather than a stub.
func installModuleWorkContextAuthority(t *testing.T) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	resetModuleWorkContextAuthority(t)
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{
		Issuer:     "accounts.test",
		KeyID:      "accounts-test-key",
		PrivateKey: privateKey,
		Authority:  &workContextAuthorityFake{},
	})
	require.NoError(t, workContextSingleton.configureErr)
}

// resetModuleWorkContextAuthority leaves the shared issuer unconfigured, and
// restores whatever it held for the next test — the singleton is global, so a
// key installed by one test must never decide another's outcome.
func resetModuleWorkContextAuthority(t *testing.T) {
	t.Helper()
	previous := *workContextSingleton
	t.Cleanup(func() { *workContextSingleton = previous })
	workContextSingleton.Configure(WorkContextAuthorityConfiguration{})
}

func installModuleWorkContextService(t *testing.T, declaredSecrets, declaredPrincipals string) *recordingModuleStore {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })
	store := &recordingModuleStore{}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	secrets, err := business.ParseRegistrationSecrets(declaredSecrets)
	require.NoError(t, err)
	svc.SetModuleRegistrar(&recordingRegistrationMinter{}, secrets)
	svc.SetModuleIdentitySecrets(secrets)
	registry, err := business.ParseModulePrincipalRegistry(declaredPrincipals)
	require.NoError(t, err)
	svc.SetModuleCapabilities(nil, nil, registry)
	WithService(svc)
	return store
}

// stampModuleWorkContext puts a signed module capability on the context the way
// a composed module forwards it.
func stampModuleWorkContext(t *testing.T, principalID, tenant string) context.Context {
	t.Helper()
	token, _, err := WorkContextSingleton().StartModuleTask(business.ModuleWorkContextAuthority{
		PrincipalID: principalID,
		Tenant:      tenant,
	})
	require.NoError(t, err)
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(codefly.WorkContextHeaderName, token.Encoded()))
}

func mintModuleWorkContext(t *testing.T, prefix, secret string) (*gen.ModuleMintWorkContextResponse, error) {
	t.Helper()
	return ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(),
		&gen.ModuleMintWorkContextRequest{Prefix: prefix, Secret: secret})
}

// The identity a module receives must be the one the surface then authenticates
// it as, so the mint and the verification are asserted as one round trip.
func TestMintModuleWorkContextRoundTripsToTheCallerIdentity(t *testing.T) {
	installModuleWorkContextService(t, "documents:"+registrationDigest("documents-secret"), documentsPrincipals)
	installModuleWorkContextAuthority(t)

	resp, err := mintModuleWorkContext(t, "documents", "documents-secret")

	require.NoError(t, err)
	require.Equal(t, business.ModulePrincipalID("documents"), resp.GetPrincipalId())
	require.Equal(t, moduleWorkContextTenant, resp.GetTenant())

	caller, err := WorkContextSingleton().VerifyModuleWorkContext(resp.GetToken())
	require.NoError(t, err)
	require.Equal(t, resp.GetPrincipalId(), caller.PrincipalID)
	require.Equal(t, moduleWorkContextTenant, caller.BoundOrg)
}

func TestMintModuleWorkContextDeniesUnconfiguredIdentity(t *testing.T) {
	installModuleRegistrar(t, "documents:"+registrationDigest("registration-secret"))
	registry, err := business.ParseModulePrincipalRegistry(documentsPrincipals)
	require.NoError(t, err)
	service.SetModuleCapabilities(nil, nil, registry)
	installModuleWorkContextAuthority(t)

	resp, err := mintModuleWorkContext(t, "documents", "registration-secret")

	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Nil(t, resp)
}

func TestModuleExchangesUseIndependentSecrets(t *testing.T) {
	for name, identityDeclaration := range map[string]string{
		"declared identity": "documents:" + registrationDigest("identity-secret"),
		"missing prefix":    "billing:" + registrationDigest("identity-secret"),
		"empty authority":   "",
	} {
		t.Run(name, func(t *testing.T) {
			store := installModuleWorkContextService(t,
				"documents:"+registrationDigest("registration-secret"), documentsPrincipals)
			secrets, err := business.ParseRegistrationSecrets(identityDeclaration)
			require.NoError(t, err)
			service.SetModuleIdentitySecrets(secrets)
			installModuleWorkContextAuthority(t)

			resp, err := mintModuleWorkContext(t, "documents", "registration-secret")
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Nil(t, resp)
			require.Zero(t, store.controlPlaneCalls)

			_, err = ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(),
				&gen.ModuleMintRegistrationRequest{Prefix: "documents", Secret: "identity-secret"})
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Zero(t, store.controlPlaneCalls)

			registration, err := ModuleCapabilitiesSingleton().MintModuleRegistration(context.Background(),
				&gen.ModuleMintRegistrationRequest{Prefix: "documents", Secret: "registration-secret"})
			require.NoError(t, err)
			require.NotEmpty(t, registration.GetToken())

			resp, err = mintModuleWorkContext(t, "documents", "identity-secret")
			if _, declared := secrets["documents"]; !declared {
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				require.Nil(t, resp)
				require.Equal(t, 1, store.controlPlaneCalls)
				return
			}
			require.NoError(t, err)
			caller, err := WorkContextSingleton().VerifyModuleWorkContext(resp.GetToken())
			require.NoError(t, err)
			require.Equal(t, business.ModulePrincipalID("documents"), caller.PrincipalID)
			require.Equal(t, moduleWorkContextTenant, caller.BoundOrg)
			require.Equal(t, 2, store.controlPlaneCalls)
		})
	}
}

// The capability seals identity and tenant only. Sealing the grant as well would
// put the same authority in two places that disagree for a token lifetime, and
// an empty scope set denies at any consumer that reads one.
func TestStartModuleTaskSealsIdentityWithoutSealingAuthority(t *testing.T) {
	installModuleWorkContextAuthority(t)
	principalID := business.ModulePrincipalID("documents")

	_, signed, err := WorkContextSingleton().StartModuleTask(business.ModuleWorkContextAuthority{
		PrincipalID: principalID,
		Tenant:      moduleWorkContextTenant,
	})

	require.NoError(t, err)
	require.Equal(t, ModuleWorkContextAudience, signed.GetAudience())
	require.Equal(t, moduleWorkContextTenant, signed.GetTenantId())
	require.Equal(t, principalID, signed.GetOwnerPrincipalId())
	require.Empty(t, signed.GetAuthorityScopes())
	require.Len(t, signed.GetActorChain(), 1)
	require.Equal(t, principalID, signed.GetActorChain()[0].GetPrincipalId())
	require.Equal(t, business.PrincipalKindService, signed.GetActorChain()[0].GetPrincipalKind())
	require.Empty(t, signed.GetActorChain()[0].GetGrantedScopes())
}

// The audit spine must never carry an issuance that did not happen: the record is
// written after the capability exists, so a mint that fails to sign records
// nothing.
func TestMintModuleWorkContextRecordsNothingWhenSigningFails(t *testing.T) {
	store := installModuleWorkContextService(t, "documents:"+registrationDigest("documents-secret"), documentsPrincipals)
	resetModuleWorkContextAuthority(t)

	_, err := mintModuleWorkContext(t, "documents", "documents-secret")

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, store.controlPlaneCalls, "a mint that never signed must not record an issuance")
}

// The converse: a capability whose issuance cannot be recorded is withheld.
func TestMintModuleWorkContextWithholdsTheCapabilityWhenTheRecordFails(t *testing.T) {
	store := installModuleWorkContextService(t, "documents:"+registrationDigest("documents-secret"), documentsPrincipals)
	installModuleWorkContextAuthority(t)
	store.controlPlaneErr = errors.New("audit store unavailable")

	resp, err := mintModuleWorkContext(t, "documents", "documents-secret")

	require.Error(t, err)
	require.Nil(t, resp)
	require.Equal(t, 1, store.controlPlaneCalls)
}

// A deployment that never wired the signing key is the operator's problem, not a
// refusal of the caller's credential, and the two must stay distinguishable.
func TestMintModuleWorkContextReportsAnUnconfiguredAuthority(t *testing.T) {
	installModuleWorkContextService(t, "documents:"+registrationDigest("documents-secret"), documentsPrincipals)
	resetModuleWorkContextAuthority(t)

	_, err := mintModuleWorkContext(t, "documents", "documents-secret")

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// The per-module binding the registration secret already carries must hold for
// this credential too: holding one module's secret yields no other's identity.
func TestMintModuleWorkContextBindsSecretToPrefix(t *testing.T) {
	installModuleWorkContextService(t,
		"documents:"+registrationDigest("documents-secret")+",billing:"+registrationDigest("billing-secret"),
		`{"documents":{"queues":["datasource"],"tenant":"`+moduleWorkContextTenant+`"},`+
			`"billing":{"queues":["billing"],"tenant":"`+moduleWorkContextTenant+`"}}`)
	installModuleWorkContextAuthority(t)

	_, err := mintModuleWorkContext(t, "billing", "documents-secret")

	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestMintModuleWorkContextFailsClosed(t *testing.T) {
	declaredSecrets := "documents:" + registrationDigest("documents-secret")
	tests := map[string]struct {
		secrets    string
		principals string
		secret     string
	}{
		"wrong secret": {declaredSecrets, documentsPrincipals, "guessed"},
		// A module may federate a REST prefix without being granted the capability
		// surface; the registration secret alone must not confer an identity here.
		"secret declared but principal is not": {declaredSecrets, "", "documents-secret"},
		"nothing declared":                     {"", "", "documents-secret"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := installModuleWorkContextService(t, test.secrets, test.principals)
			secrets, err := business.ParseRegistrationSecrets(test.secrets)
			require.NoError(t, err)
			service.SetModuleIdentitySecrets(secrets)
			installModuleWorkContextAuthority(t)

			_, err = mintModuleWorkContext(t, "documents", test.secret)

			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Zero(t, store.controlPlaneCalls, "a refused mint must not record an issuance")
		})
	}
}

// The surface takes its caller from the signed capability, never from metadata a
// caller on the internal listener can set for itself.
func TestModuleCallerRequiresAVerifiableWorkContext(t *testing.T) {
	installModuleWorkContextAuthority(t)
	tests := map[string]context.Context{
		"no metadata":  context.Background(),
		"no header":    metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-user-id", "someone")),
		"empty header": metadata.NewIncomingContext(context.Background(), metadata.Pairs(codefly.WorkContextHeaderName, "")),
		"not a token": metadata.NewIncomingContext(context.Background(),
			metadata.Pairs(codefly.WorkContextHeaderName, "not-a-capability")),
	}
	for name, ctx := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := moduleCaller(ctx)

			require.Equal(t, codes.Unauthenticated, status.Code(err))
		})
	}
}

// A capability signed by a key this issuer does not hold must not authenticate,
// which is the property that makes the token — rather than the caller's word —
// the identity.
func TestModuleCallerRejectsAForeignlySignedWorkContext(t *testing.T) {
	installModuleWorkContextAuthority(t)
	ctx := stampModuleWorkContext(t, business.ModulePrincipalID("documents"), moduleWorkContextTenant)
	installModuleWorkContextAuthority(t) // re-keys the issuer under a different keypair

	_, err := moduleCaller(ctx)

	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestModuleCallerReadsTheVerifiedPrincipalAndTenant(t *testing.T) {
	installModuleWorkContextAuthority(t)
	principalID := business.ModulePrincipalID("documents")

	caller, err := moduleCaller(stampModuleWorkContext(t, principalID, moduleWorkContextTenant))

	require.NoError(t, err)
	require.Equal(t, principalID, caller.PrincipalID)
	require.Equal(t, moduleWorkContextTenant, caller.BoundOrg)
}
