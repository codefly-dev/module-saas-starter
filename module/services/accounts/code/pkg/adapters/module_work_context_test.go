package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/business/executiontest"
	"accounts/pkg/business/moduleregistrytest"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	moduleWorkContextTenant = "55555555-5555-4555-8555-555555555555"
	documentsPrincipals     = `{"documents":{"queues":["datasource"],"namespaces":["document"],"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` +
		moduleWorkContextTenant + `"}}`
)

// recordingModuleStore observes the control-plane transaction the issuance
// record is written in, so a test can tell whether the record was attempted at
// all and force it to fail.
type recordingModuleStore struct {
	business.Store
	controlPlaneCalls int
	controlPlaneErr   error
	// tenantExists answers the declared tenant's existence check. Default it
	// to true wherever a mint is expected to succeed: the check is a real
	// database read in production, and a fake that reported "absent" would
	// make every mint fail closed.
	tenantExists    bool
	tenantChecks    int
	tenantCheckedID string
}

// WithControlPlane serves two callers in the mint flow, in this order: the
// declared tenant's existence check, then the issuance record. controlPlaneErr
// targets the record — the case the tests using it are about — so it is
// applied only once the tenant check has run.
func (s *recordingModuleStore) WithControlPlane(ctx context.Context, fn func(ctx context.Context) error) error {
	s.controlPlaneCalls++
	if s.controlPlaneErr != nil && s.tenantChecks > 0 {
		return s.controlPlaneErr
	}
	return fn(ctx)
}

func (s *recordingModuleStore) OrganizationIDExists(_ context.Context, id string) (bool, error) {
	s.tenantChecks++
	s.tenantCheckedID = id
	return s.tenantExists, nil
}

// recordAttempts is the control-plane transactions that were the issuance
// record rather than the tenant check.
func (s *recordingModuleStore) recordAttempts() int {
	return s.controlPlaneCalls - s.tenantChecks
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

func installModuleWorkContextService(t *testing.T, declaredPrincipals string) *recordingModuleStore {
	t.Helper()
	previous := service
	t.Cleanup(func() { service = previous })
	store := &recordingModuleStore{tenantExists: true}
	svc, err := business.NewService(store)
	require.NoError(t, err)
	registry, err := moduleregistrytest.Parse(declaredPrincipals)
	require.NoError(t, err)
	svc.SetModuleAuthorityReads(currentModuleAuthority{service: svc}, nil)
	svc.SetModuleCapabilities(nil, nil, registry)
	bindModuleExecution(t, svc, registry)
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
		metadata.Pairs(workcontext.WorkContextHeaderName, token.Encoded()))
}

func mintModuleWorkContext(t *testing.T, prefix, token string) (*gen.ModuleMintWorkContextResponse, error) {
	t.Helper()
	return ModuleCapabilitiesSingleton().MintModuleWorkContext(context.Background(),
		&gen.ModuleMintWorkContextRequest{Prefix: prefix, ServiceAccountToken: token})
}

// The identity a module receives must be the one the surface then authenticates
// it as, so the mint and the verification are asserted as one round trip.
func TestMintModuleWorkContextRoundTripsToTheCallerIdentity(t *testing.T) {
	installModuleWorkContextService(t, documentsPrincipals)
	installModuleWorkContextAuthority(t)

	resp, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))

	require.NoError(t, err)
	require.Equal(t, business.ModulePrincipalID("documents"), resp.GetPrincipalId())
	require.Equal(t, moduleWorkContextTenant, resp.GetTenant())

	caller, err := WorkContextSingleton().VerifyModuleWorkContext(resp.GetToken())
	require.NoError(t, err)
	require.Equal(t, resp.GetPrincipalId(), caller.PrincipalID)
	require.Equal(t, moduleWorkContextTenant, caller.BoundOrg)
}

// A host with no execution binding wired cannot establish what any caller is
// running, so it mints nothing — and says so as Unavailable, never as a verdict
// about the caller, and never as a capability minted unbound.
func TestMintModuleWorkContextIsUnavailableWithoutExecutionBinding(t *testing.T) {
	store := installModuleWorkContextService(t, documentsPrincipals)
	token := moduleToken(t, "documents")
	service.SetExecutionBinding(nil, nil)
	installModuleWorkContextAuthority(t)

	resp, err := mintModuleWorkContext(t, "documents", token)

	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Nil(t, resp)
	require.Zero(t, store.controlPlaneCalls, "nothing may be checked, signed or recorded")
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
	store := installModuleWorkContextService(t, documentsPrincipals)
	resetModuleWorkContextAuthority(t)

	_, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, store.recordAttempts(), "a mint that never signed must not record an issuance")
}

// The converse: a capability whose issuance cannot be recorded is withheld.
func TestMintModuleWorkContextWithholdsTheCapabilityWhenTheRecordFails(t *testing.T) {
	store := installModuleWorkContextService(t, documentsPrincipals)
	installModuleWorkContextAuthority(t)
	store.controlPlaneErr = errors.New("audit store unavailable")

	resp, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))

	require.Error(t, err)
	require.Nil(t, resp)
	require.Equal(t, 1, store.recordAttempts())
}

// A declared tenant that names no organization must not mint. Everything
// upstream validates the tenant's form only — the registry checks a form,
// ModuleAuthorizeWorkContext copies the value through, and authorizeTenant
// compares the sealed tenant against
// itself — so without this check a typo'd or wrong-environment uuid signs
// cleanly and binds every later call to a tenant that is not there. It stays
// silent too: audit_events.org_id and domain_events.tenant_id have no foreign
// key to organizations, so the writes land without complaint.
func TestMintModuleWorkContextRefusesATenantThatNamesNoOrganization(t *testing.T) {
	store := installModuleWorkContextService(t, documentsPrincipals)
	installModuleWorkContextAuthority(t)
	store.tenantExists = false

	resp, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))

	require.Error(t, err)
	require.Nil(t, resp, "no capability may be handed out for a tenant that does not exist")
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), moduleWorkContextTenant,
		"the operator has to be told which tenant could not be resolved")
	require.Equal(t, moduleWorkContextTenant, store.tenantCheckedID)
	require.Zero(t, store.recordAttempts(),
		"an issuance that was refused must not reach the audit spine")
}

// The check runs against the tenant the grant declares, not one the caller
// names: a module cannot get itself admitted by asking for a tenant that does
// exist.
func TestMintModuleWorkContextChecksTheDeclaredTenantNotARequestedOne(t *testing.T) {
	store := installModuleWorkContextService(t, documentsPrincipals)
	installModuleWorkContextAuthority(t)

	_, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))

	require.NoError(t, err)
	require.Equal(t, 1, store.tenantChecks)
	require.Equal(t, moduleWorkContextTenant, store.tenantCheckedID)
}

// A deployment that never wired the signing key is the operator's problem, not a
// refusal of the caller's credential, and the two must stay distinguishable.
func TestMintModuleWorkContextReportsAnUnconfiguredAuthority(t *testing.T) {
	installModuleWorkContextService(t, documentsPrincipals)
	resetModuleWorkContextAuthority(t)

	_, err := mintModuleWorkContext(t, "documents", moduleToken(t, "documents"))

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// One module's pod cannot mint as another prefix: its token authenticates its
// own ServiceAccount, which identifies exactly one principal, and that is not
// the one it asked for. (Two principals SHARING a ServiceAccount mint as
// neither — TestAWorkloadSharedByTwoPrincipalsMintsAsNeither.)
func TestMintModuleWorkContextBindsTheTokenToItsOwnPrefix(t *testing.T) {
	store := installModuleWorkContextService(t,
		`{"documents":{"queues":["datasource"],"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"`+moduleWorkContextTenant+`"},`+
			`"billing":{"queues":["billing"],"workload":{"service_account":"billing","namespace":"acme-prod","container":"app"},"tenant":"`+moduleWorkContextTenant+`"}}`)
	installModuleWorkContextAuthority(t)

	_, err := mintModuleWorkContext(t, "billing", moduleToken(t, "documents"))

	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Zero(t, store.controlPlaneCalls)
}

// Every way a caller can fail to be the module it names, at the mint itself:
// each mints nothing, records nothing, and answers a verdict — except an
// unreachable API server, which is not one.
func TestMintModuleWorkContextIsBoundToTheRunningBuild(t *testing.T) {
	declared := executiontest.Workload{Namespace: "acme-prod", ServiceAccount: "module", Container: "app"}
	tests := map[string]struct {
		token func(cluster *executiontest.Cluster) string
		want  codes.Code
	}{
		"a pod on an unapproved build": {func(c *executiontest.Cluster) string {
			return c.Run(declared, executiontest.Image("documents-unreviewed"))
		}, codes.PermissionDenied},
		"a moved tag: asked for the approved image, running another": {func(c *executiontest.Cluster) string {
			token := c.Run(declared, executiontest.Image("documents"))
			c.Retag(token, executiontest.Image("documents-unreviewed"))
			return token
		}, codes.PermissionDenied},
		"a token for another ServiceAccount": {func(c *executiontest.Cluster) string {
			return c.Run(executiontest.Workload{Namespace: "acme-prod", ServiceAccount: "intruder", Container: "app"},
				executiontest.Image("documents"))
		}, codes.PermissionDenied},
		"a token from another namespace": {func(c *executiontest.Cluster) string {
			return c.Run(executiontest.Workload{Namespace: "other-tenant", ServiceAccount: "module", Container: "app"},
				executiontest.Image("documents"))
		}, codes.PermissionDenied},
		"a token minted for another audience": {func(c *executiontest.Cluster) string {
			return c.RunWithAudience(declared, executiontest.Image("documents"), "vault")
		}, codes.PermissionDenied},
		"a replacement pod at the token's pod name": {func(c *executiontest.Cluster) string {
			token := c.Run(declared, executiontest.Image("documents"))
			c.Replace(token)
			return token
		}, codes.PermissionDenied},
		"something that is not a token": {func(*executiontest.Cluster) string {
			return "documents-secret"
		}, codes.PermissionDenied},
		"the api server is unreachable": {func(c *executiontest.Cluster) string {
			token := c.Run(declared, executiontest.Image("documents"))
			c.Unreachable = true
			return token
		}, codes.Unavailable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := installModuleWorkContextService(t, documentsPrincipals)
			installModuleWorkContextAuthority(t)

			resp, err := mintModuleWorkContext(t, "documents", test.token(moduleExecution.cluster))

			require.Equal(t, test.want, status.Code(err), "%v", err)
			require.Nil(t, resp)
			require.Zero(t, store.controlPlaneCalls, "a refused mint must not check, sign or record anything")
		})
	}
}

func TestMintModuleWorkContextFailsClosed(t *testing.T) {
	tests := map[string]struct {
		principals string
		prefix     string
	}{
		// A prefix nothing declares names no workload, so no token can back it.
		"principal not declared": {"", "documents"},
		"malformed prefix":       {documentsPrincipals, "Documents/.."},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := installModuleWorkContextService(t, documentsPrincipals)
			token := moduleToken(t, "documents")
			registry, err := moduleregistrytest.Parse(test.principals)
			require.NoError(t, err)
			service.SetModuleCapabilities(nil, nil, registry)
			installModuleWorkContextAuthority(t)

			_, err = mintModuleWorkContext(t, test.prefix, token)

			require.Contains(t, []codes.Code{codes.PermissionDenied, codes.InvalidArgument}, status.Code(err))
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
		"empty header": metadata.NewIncomingContext(context.Background(), metadata.Pairs(workcontext.WorkContextHeaderName, "")),
		"not a token": metadata.NewIncomingContext(context.Background(),
			metadata.Pairs(workcontext.WorkContextHeaderName, "not-a-capability")),
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

// Exercise the real TokenReview/BindExecution path through the mint adapter.
// A refusal happens before signing or recording a credential.
func TestWorkloadPeerMintMatchesReviewedTokenAndDeliveredPresence(t *testing.T) {
	const peer = "spiffe://acme.test/ns/acme-prod/sa/module"
	declared := strings.Replace(documentsPrincipals, `"workload":{`, `"workload":{"spiffe_id":"`+peer+`",`, 1)
	for _, test := range []struct {
		name, peer, declaration string
		allowed                 bool
	}{
		{"matching", peer, declared, true},
		{"another service account", "spiffe://acme.test/ns/acme-prod/sa/other", declared, false},
		{"another namespace", "spiffe://acme.test/ns/other/sa/module", declared, false},
		{"another trust domain", "spiffe://other.test/ns/acme-prod/sa/module", declared, false},
		{"absent declared identity", peer, documentsPrincipals, false},
		{"empty attestation", "", declared, false},
		// TLS and presence agree, but the reviewed token names another SA.
		{"presence differs from reviewed token", peer, strings.Replace(declared, `"service_account":"module"`, `"service_account":"other"`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := installModuleWorkContextService(t, test.declaration)
			installModuleWorkContextAuthority(t)
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-workload-spiffe-id", test.peer))
			response, err := ModuleCapabilitiesSingleton().MintModuleWorkContext(ctx, &gen.ModuleMintWorkContextRequest{Prefix: "documents", ServiceAccountToken: moduleToken(t, "documents")})
			if test.allowed {
				require.NoError(t, err)
				require.NotEmpty(t, response.GetToken())
				require.Equal(t, 1, store.recordAttempts())
			} else {
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				require.Nil(t, response)
				require.Zero(t, store.recordAttempts())
			}
		})
	}
}
