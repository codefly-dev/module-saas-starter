package adapters

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	corework "github.com/codefly-dev/core/workcontext"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// callingModuleContext is how a composed module asks a Work Context read
// oracle: its own module Work Context for principalID in tenant, minted by the
// issuer that will verify it and sealed to the installation the live module
// authority answers.
func callingModuleContext(t *testing.T, issuer *WorkContextAuthorityServer, principalID, tenant string) context.Context {
	t.Helper()
	installation := "installation-" + principalID
	if service != nil {
		if live, _, err := service.WorkContextSeal(context.Background(), tenant, principalID, ""); err == nil {
			installation = live
		}
	}
	token, _, err := issuer.StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{
		PrincipalID:    principalID,
		Tenant:         tenant,
		InstallationID: installation,
	})
	require.NoError(t, err)
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(workContextHeader, token))
}

// The refusals and the acceptance of the read oracles' caller authentication,
// each on both oracles: a shared internal token is never a caller, a module
// Work Context that does not verify is refused, one naming a module the host
// does not declare is refused, and a host that cannot authenticate modules says
// so rather than answering.
func TestWorkContextReadOraclesAuthenticateTheCallingModule(t *testing.T) {
	installModuleWorkContextService(t, documentsPrincipals)
	installModuleWorkContextAuthority(t)
	server := WorkContextSingleton()
	previous := server.consumer
	server.consumer = &workContextConsumerAuthorityFake{}
	t.Cleanup(func() { server.consumer = previous })

	const (
		owner  = "019fec91-1000-7000-8000-000000000002"
		caller = "019fec91-1000-7000-8000-000000000004"
	)
	sealsNone := true
	oracles := map[string]func(context.Context) error{
		"CheckAuthorizationRevision": func(ctx context.Context) error {
			_, err := server.CheckAuthorizationRevision(ctx, &gen.CheckAuthorizationRevisionRequest{
				OrgId: moduleWorkContextTenant, OwnerPrincipalId: owner, AuthorizationRevision: 12,
				Subjects: []*gen.WorkContextRevisionSubject{{PrincipalId: owner}}, SealsNoBinding: &sealsNone,
			})
			return err
		},
		"AuthorizeEvidenceRead": func(ctx context.Context) error {
			_, err := server.AuthorizeEvidenceRead(ctx, &gen.AuthorizeEvidenceReadRequest{
				OrgId: moduleWorkContextTenant, CallerPrincipalId: caller,
			})
			return err
		},
	}

	SetInternalToken("read-oracle-test-token")
	t.Cleanup(func() { SetInternalToken("") })
	documents := business.ModulePrincipalID("documents")
	refusals := []struct {
		name    string
		ctx     func(t *testing.T) context.Context
		code    codes.Code
		message string
	}{
		{"no credential", func(*testing.T) context.Context { return context.Background() },
			codes.Unauthenticated, errOracleCallerUnidentified},
		{"the shared internal token alone", func(*testing.T) context.Context {
			return metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-codefly-internal-token", "read-oracle-test-token"))
		}, codes.Unauthenticated, errOracleCallerUnidentified},
		{"two module Work Contexts", func(t *testing.T) context.Context {
			one := callingModuleContext(t, server, documents, moduleWorkContextTenant)
			md, _ := metadata.FromIncomingContext(one)
			token := md.Get(workContextHeader)[0]
			return metadata.NewIncomingContext(context.Background(), metadata.Pairs(workContextHeader, token, workContextHeader, token))
		}, codes.Unauthenticated, errOracleCallerUnidentified},
		{"a Work Context that does not verify", func(*testing.T) context.Context {
			return metadata.NewIncomingContext(context.Background(), metadata.Pairs(workContextHeader, "not-a-capability"))
		}, codes.Unauthenticated, "module work context is not a valid capability"},
		{"a Work Context another issuer signed", func(t *testing.T) context.Context {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			foreign := &WorkContextAuthorityServer{}
			foreign.Configure(WorkContextAuthorityConfiguration{
				Issuer: "accounts.test", KeyID: "accounts-test-key", PrivateKey: key,
				Authority: &workContextAuthorityFake{}, Seals: moduleSealAnswers(12),
			})
			require.NoError(t, foreign.configureErr)
			return callingModuleContext(t, foreign, documents, moduleWorkContextTenant)
		}, codes.Unauthenticated, "module work context is not a valid capability"},
		{"a module the host does not declare", func(t *testing.T) context.Context {
			return callingModuleContext(t, server, business.ModulePrincipalID("intruder"), moduleWorkContextTenant)
		}, codes.Unauthenticated, "is not a registered module principal"},
	}
	for name, ask := range oracles {
		for _, refusal := range refusals {
			t.Run(name+"/"+refusal.name, func(t *testing.T) {
				err := ask(refusal.ctx(t))
				require.Equal(t, refusal.code, status.Code(err), "%v", err)
				require.Contains(t, status.Convert(err).Message(), refusal.message)
			})
		}
		t.Run(name+"/the declared module", func(t *testing.T) {
			require.NoError(t, ask(callingModuleContext(t, server, documents, moduleWorkContextTenant)))
		})
	}

	// A host with no module authority cannot tell a module from anyone else.
	t.Run("no module authority", func(t *testing.T) {
		moduleCtx := callingModuleContext(t, server, documents, moduleWorkContextTenant)
		previousService := service
		service = nil
		t.Cleanup(func() { service = previousService })
		for name, ask := range oracles {
			err := ask(moduleCtx)
			require.Equal(t, codes.Unavailable, status.Code(err), name)
			require.Contains(t, status.Convert(err).Message(), errOracleCallerUnavailable, name)
		}
	})
}

// callingModuleTokenFor is the module Work Context of the module a viewer
// capability is addressed to — its audience is that module's prefix — minted
// by the shared issuer in the viewer's tenant. It is how a module asks about a
// viewer it serves: its own identity in the header, the viewer in the body.
func callingModuleTokenFor(viewer string) string {
	decoded, err := corework.Decode(viewer)
	if err != nil {
		// A viewer that does not decode names no module; the call is then made
		// with no module identity and must be refused for that.
		return ""
	}
	claims := decoded.Context()
	principal := business.ModulePrincipalID(claims.GetAudience())
	installation := "installation-" + principal
	if service != nil {
		if live, _, err := service.WorkContextSeal(context.Background(), claims.GetTenantId(), principal, ""); err == nil {
			installation = live
		}
	}
	token, _, err := WorkContextSingleton().StartModuleTask(context.Background(), business.ModuleWorkContextAuthority{
		PrincipalID: principal, Tenant: claims.GetTenantId(), InstallationID: installation,
	})
	if err != nil {
		panic("minting the calling module's identity: " + err.Error())
	}
	return token
}
