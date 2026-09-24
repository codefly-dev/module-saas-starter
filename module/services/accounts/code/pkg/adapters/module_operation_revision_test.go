package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const revisionTestInternalToken = "module-operation-revision-test-token"

func revisionTestContext(t *testing.T) context.Context {
	t.Helper()
	SetInternalToken(revisionTestInternalToken)
	t.Cleanup(func() { SetInternalToken("") })
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-codefly-internal-token", revisionTestInternalToken))
}

func revisionScopes(scopes []*basev0.WorkScopeV1) []*gen.WorkContextScope {
	out := make([]*gen.WorkContextScope, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, &gen.WorkContextScope{
			ResourceKind: scope.GetResourceKind(),
			Actions:      append([]string(nil), scope.GetActions()...),
			ResourceIds:  append([]string(nil), scope.GetResourceIds()...),
		})
	}
	return out
}

// revisionRequestFromClaims builds the request exactly as a consumer's SDK does:
// the owner with the authority scopes, then every actor with its granted
// scopes, and the revision the token carries.
func revisionRequestFromClaims(signed *basev0.WorkContextV1) *gen.CheckAuthorizationRevisionRequest {
	subjects := []*gen.WorkContextRevisionSubject{{
		PrincipalId: signed.GetOwnerPrincipalId(),
		Scopes:      revisionScopes(signed.GetAuthorityScopes()),
	}}
	for _, actor := range signed.GetActorChain() {
		subjects = append(subjects, &gen.WorkContextRevisionSubject{
			PrincipalId: actor.GetPrincipalId(),
			Scopes:      revisionScopes(actor.GetGrantedScopes()),
		})
	}
	return &gen.CheckAuthorizationRevisionRequest{
		OrgId:                 signed.GetTenantId(),
		OwnerPrincipalId:      signed.GetOwnerPrincipalId(),
		AuthorizationRevision: signed.GetAuthorizationRevision(),
		Subjects:              subjects,
	}
}

func mintedOperationContextClaims(t *testing.T) *basev0.WorkContextV1 {
	t.Helper()
	resp, err := mintModuleOperationContext("documents", "documents-secret", "model")
	require.NoError(t, err)
	return verifyOperationContext(t, resp.GetToken())
}

// The failure this pins: a consumer confirming a freshly minted operation
// context against current authority was answered with an error that was not a
// denial, and read it as an authority outage. The context must confirm.
func TestCheckAuthorizationRevisionConfirmsAMintedModuleOperationContext(t *testing.T) {
	installModuleOperationContextService(t, "documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	installModuleWorkContextAuthority(t)
	ctx := revisionTestContext(t)

	signed := mintedOperationContextClaims(t)
	require.NotZero(t, signed.GetAuthorizationRevision(), "zero is not a revision a consumer can check")

	request := revisionRequestFromClaims(signed)
	require.NoError(t, Validate(request))
	_, err := WorkContextSingleton().CheckAuthorizationRevision(ctx, request)
	require.NoError(t, err)
}

func TestCheckAuthorizationRevisionDeniesAnyModuleOperationContextMismatch(t *testing.T) {
	installModuleOperationContextService(t, "documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	installModuleWorkContextAuthority(t)
	ctx := revisionTestContext(t)
	signed := mintedOperationContextClaims(t)
	owner := signed.GetOwnerPrincipalId()

	for name, mutate := range map[string]func(*gen.CheckAuthorizationRevisionRequest){
		"another tenant": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.OrgId = "019fec91-1000-7000-8000-000000000009"
		},
		"another revision": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.AuthorizationRevision++
		},
		"another actor": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.Subjects[1].PrincipalId = "019fec91-1000-7000-8000-000000000003"
		},
		"an extra actor": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.Subjects = append(r.Subjects, &gen.WorkContextRevisionSubject{PrincipalId: "019fec91-1000-7000-8000-000000000003"})
		},
		"an action beyond headless": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.Subjects[0].Scopes[0].Actions = []string{"delete", "invoke"}
		},
		"a resource beyond headless": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.Subjects[1].Scopes[0].ResourceIds = []string{"other-profile"}
		},
		"kind-wide where headless pins a resource": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.Subjects[0].Scopes[0].ResourceIds = nil
		},
		"a kind no binding declares headless": func(r *gen.CheckAuthorizationRevisionRequest) {
			r.Subjects[0].Scopes = append(r.Subjects[0].Scopes, &gen.WorkContextScope{ResourceKind: "reports", Actions: []string{"read"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := revisionRequestFromClaims(signed)
			mutate(request)
			require.Equal(t, owner, request.GetSubjects()[0].GetPrincipalId())

			_, err := WorkContextSingleton().CheckAuthorizationRevision(ctx, request)

			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		})
	}
}

// A narrower subset of the headless grant still confirms: a consumer may check
// only what it is about to use.
func TestCheckAuthorizationRevisionAcceptsANarrowerModuleOperationSubject(t *testing.T) {
	installModuleOperationContextService(t, "documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	installModuleWorkContextAuthority(t)
	ctx := revisionTestContext(t)
	request := revisionRequestFromClaims(mintedOperationContextClaims(t))
	request.Subjects[1].Scopes[0].Actions = []string{"invoke"}

	_, err := WorkContextSingleton().CheckAuthorizationRevision(ctx, request)
	require.NoError(t, err)
}

// Changing the module's declaration revokes every context minted under the old
// one, whether the change narrows the binding or touches an unrelated key.
func TestCheckAuthorizationRevisionRevokesModuleOperationContextsOnConfigChange(t *testing.T) {
	for name, changed := range map[string]string{
		"headless grant removed": strings.Replace(headlessPrincipals,
			`,`+`"headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"],"resource_ids":["profile-digest"]}]`, ``, 1),
		"unrelated key changed": strings.Replace(headlessPrincipals,
			`{"documents":{`, `{"documents":{"queues":["datasource"],`, 1),
		"module removed": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, headlessPrincipals, changed)
			installModuleOperationContextService(t, "documents:"+registrationDigest("documents-secret"), headlessPrincipals)
			installModuleWorkContextAuthority(t)
			ctx := revisionTestContext(t)
			request := revisionRequestFromClaims(mintedOperationContextClaims(t))

			// The deployment restarts with a changed declaration; the signing key
			// is unchanged, so only the declaration can revoke the context.
			registry, err := business.ParseModulePrincipalRegistry(changed)
			require.NoError(t, err)
			service.SetModuleCapabilities(nil, nil, registry)

			_, err = WorkContextSingleton().CheckAuthorizationRevision(ctx, request)
			if name == "module removed" {
				// No longer a module principal: the owner is judged by the
				// row-backed path, which here has no consumer store at all. It is
				// not confirmed either way.
				require.Error(t, err)
				return
			}
			require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
		})
	}
}

// Every other owner — a person, or an installation — keeps taking the
// row-backed check, even while module principals are declared.
func TestCheckAuthorizationRevisionLeavesNonModuleOwnersToTheStore(t *testing.T) {
	installModuleOperationContextService(t, "documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	fake := &workContextConsumerAuthorityFake{}
	server := &WorkContextAuthorityServer{}
	server.Configure(WorkContextAuthorityConfiguration{
		Issuer: "accounts.test", KeyID: "accounts-test-key", PrivateKey: privateKey, Authority: fake,
	})
	require.NoError(t, server.configureErr)
	ctx := revisionTestContext(t)
	ownerID := "019fec91-1000-7000-8000-000000000002"
	actorID := "019fec91-1000-7000-8000-000000000003"

	_, err = server.CheckAuthorizationRevision(ctx, &gen.CheckAuthorizationRevisionRequest{
		OrgId:                 moduleWorkContextTenant,
		OwnerPrincipalId:      ownerID,
		AuthorizationRevision: 7,
		Subjects:              []*gen.WorkContextRevisionSubject{{PrincipalId: ownerID}, {PrincipalId: actorID}},
	})

	require.NoError(t, err)
	require.Equal(t, ownerID, fake.revisionOwner)
	require.Equal(t, uint64(7), fake.revisionExpected)
}
