package adapters

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// headlessPrincipals declares one module with two operation bindings: one whose
// headless grant pins a single resource its invoke grant leaves kind-wide, and
// one declared for use on a person's behalf only.
const headlessPrincipals = `{"documents":{"tenant":"` + moduleWorkContextTenant + `","operation_audiences":{` +
	`"model":{"audience":"modelservice",` +
	`"invoke_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"]}],` +
	`"lookup_scopes":[{"resource_kind":"modelservice.profiles","actions":["read"]}],` +
	`"headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"],"resource_ids":["profile-digest"]}]},` +
	`"interactive":{"audience":"reportservice",` +
	`"invoke_scopes":[{"resource_kind":"reports","actions":["read","write"]}],` +
	`"lookup_scopes":[{"resource_kind":"reports","actions":["read"]}]}}}}`

// validatingAudit records every entry and rejects any payload the registry
// would, the way the durable emitter does in production: an undeclared payload
// key there drops the record with only a log line, so a test double that
// accepted it would prove nothing.
type validatingAudit struct {
	mu      sync.Mutex
	entries []business.AuditEntry
}

func (a *validatingAudit) Emit(_ context.Context, entry business.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, entry)
}

func (a *validatingAudit) EmitTx(ctx context.Context, entry business.AuditEntry) error {
	if err := business.ValidatePayload(entry.EventType, entry.Payload); err != nil {
		return err
	}
	a.Emit(ctx, entry)
	return nil
}

func (a *validatingAudit) of(event business.EventType) []business.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []business.AuditEntry
	for _, entry := range a.entries {
		if entry.EventType == event {
			out = append(out, entry)
		}
	}
	return out
}

func installModuleOperationContextService(t *testing.T, declaredSecrets, declaredPrincipals string) (*recordingModuleStore, *validatingAudit) {
	t.Helper()
	store := installModuleWorkContextService(t, declaredSecrets, declaredPrincipals)
	audit := &validatingAudit{}
	service.SetAuditEmitter(audit)
	return store, audit
}

func mintModuleOperationContext(prefix, secret, binding string) (*gen.ModuleMintOperationContextResponse, error) {
	return ModuleCapabilitiesSingleton().MintModuleOperationContext(context.Background(),
		&gen.ModuleMintOperationContextRequest{Prefix: prefix, Secret: secret, Binding: binding})
}

func verifyOperationContext(t *testing.T, token string) *basev0.WorkContextV1 {
	t.Helper()
	parsed, err := codefly.ParseWorkContextToken(token)
	require.NoError(t, err)
	verified, err := WorkContextSingleton().verifier.Verify(parsed, codefly.WorkContextExpectations{
		Issuer:   WorkContextSingleton().issuer,
		Audience: "modelservice",
	})
	require.NoError(t, err)
	return verified
}

// The minted capability carries exactly the binding's headless grant — not its
// invoke scopes, not its lookup scopes — for the binding's audience, owned and
// actored by the module's own service principal on its declared tenant.
func TestMintModuleOperationContextSealsExactlyTheHeadlessScopes(t *testing.T) {
	store, audit := installModuleOperationContextService(t,
		"documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	installModuleWorkContextAuthority(t)
	principalID := business.ModulePrincipalID("documents")

	resp, err := mintModuleOperationContext("documents", "documents-secret", "model")

	require.NoError(t, err)
	require.Equal(t, principalID, resp.GetPrincipalId())
	require.Equal(t, moduleWorkContextTenant, resp.GetTenant())
	require.Equal(t, "modelservice", resp.GetAudience())
	require.Equal(t, "model", resp.GetBinding())
	require.WithinDuration(t, time.Now().Add(business.ModuleOperationContextTTL), resp.GetExpiresAt().AsTime(), 2*time.Second)

	signed := verifyOperationContext(t, resp.GetToken())
	require.Equal(t, "modelservice", signed.GetAudience())
	require.Equal(t, moduleWorkContextTenant, signed.GetTenantId())
	require.Equal(t, principalID, signed.GetOwnerPrincipalId())
	require.Equal(t, codefly.WorkContextReplayIdempotent, signed.GetReplayPolicy())
	require.LessOrEqual(t, signed.GetExpiresAtUnix()-signed.GetIssuedAtUnix(), int64(business.ModuleOperationContextTTL/time.Second))
	want := []*basev0.WorkScopeV1{{ResourceKind: "modelservice.profiles", Actions: []string{"invoke", "read"}, ResourceIds: []string{"profile-digest"}}}
	requireScopes(t, want, signed.GetAuthorityScopes())
	require.Len(t, signed.GetActorChain(), 1)
	actor := signed.GetActorChain()[0]
	require.Equal(t, principalID, actor.GetPrincipalId())
	require.Equal(t, business.PrincipalKindService, actor.GetPrincipalKind())
	requireScopes(t, want, actor.GetGrantedScopes())

	// It is not a module identity: the capability surface refuses it.
	_, err = WorkContextSingleton().VerifyModuleWorkContext(resp.GetToken())
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	entries := audit.of(business.EventModuleOperationContextMint)
	require.Len(t, entries, 1)
	require.Equal(t, principalID, entries[0].ActorID)
	require.Equal(t, moduleWorkContextTenant, entries[0].OrgID)
	require.Equal(t, "documents", entries[0].ResourceID)
	require.Equal(t, map[string]any{
		"prefix":     "documents",
		"tenant":     moduleWorkContextTenant,
		"binding_id": "model",
		"audience":   "modelservice",
		"scopes":     []string{"modelservice.profiles:invoke:profile-digest", "modelservice.profiles:read:profile-digest"},
	}, entries[0].Payload)
	require.Equal(t, 1, store.tenantChecks)
	require.Equal(t, moduleWorkContextTenant, store.tenantCheckedID)
}

func requireScopes(t *testing.T, want, got []*basev0.WorkScopeV1) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].GetResourceKind(), got[i].GetResourceKind())
		require.Equal(t, want[i].GetActions(), got[i].GetActions())
		require.Equal(t, want[i].GetResourceIds(), got[i].GetResourceIds())
	}
}

func TestMintModuleOperationContextRefusals(t *testing.T) {
	declared := "documents:" + registrationDigest("documents-secret")
	tests := map[string]struct {
		secrets    string
		principals string
		prefix     string
		secret     string
		binding    string
		code       codes.Code
	}{
		"binding not declared":             {declared, headlessPrincipals, "documents", "documents-secret", "absent", codes.PermissionDenied},
		"binding declares no headless use": {declared, headlessPrincipals, "documents", "documents-secret", "interactive", codes.PermissionDenied},
		"wrong secret":                     {declared, headlessPrincipals, "documents", "guessed", "model", codes.Unauthenticated},
		"unknown module":                   {declared + ",billing:" + registrationDigest("billing-secret"), headlessPrincipals, "billing", "billing-secret", "model", codes.Unauthenticated},
		"another module's secret":          {declared + ",billing:" + registrationDigest("billing-secret"), headlessPrincipals, "documents", "billing-secret", "model", codes.Unauthenticated},
		"no identity digests":              {"", headlessPrincipals, "documents", "documents-secret", "model", codes.Unauthenticated},
		"invalid binding name":             {declared, headlessPrincipals, "documents", "documents-secret", "", codes.InvalidArgument},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store, audit := installModuleOperationContextService(t, test.secrets, test.principals)
			installModuleWorkContextAuthority(t)

			resp, err := mintModuleOperationContext(test.prefix, test.secret, test.binding)

			require.Equal(t, test.code, status.Code(err), "%v", err)
			require.Nil(t, resp)
			require.Zero(t, store.controlPlaneCalls, "a refused mint touches neither the tenant nor the audit spine")
			require.Empty(t, audit.of(business.EventModuleOperationContextMint))
		})
	}
}

func TestMintModuleOperationContextRefusesATenantThatNamesNoOrganization(t *testing.T) {
	store, audit := installModuleOperationContextService(t,
		"documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	installModuleWorkContextAuthority(t)
	store.tenantExists = false

	resp, err := mintModuleOperationContext("documents", "documents-secret", "model")

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Nil(t, resp)
	require.Empty(t, audit.of(business.EventModuleOperationContextMint))
}

func TestMintModuleOperationContextRecordsNothingWhenSigningFails(t *testing.T) {
	store, audit := installModuleOperationContextService(t,
		"documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	resetModuleWorkContextAuthority(t)

	_, err := mintModuleOperationContext("documents", "documents-secret", "model")

	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, store.recordAttempts())
	require.Empty(t, audit.of(business.EventModuleOperationContextMint))
}

func TestMintModuleOperationContextWithholdsTheCapabilityWhenTheRecordFails(t *testing.T) {
	store, _ := installModuleOperationContextService(t,
		"documents:"+registrationDigest("documents-secret"), headlessPrincipals)
	installModuleWorkContextAuthority(t)
	store.controlPlaneErr = errors.New("audit store unavailable")

	resp, err := mintModuleOperationContext("documents", "documents-secret", "model")

	require.Error(t, err)
	require.Nil(t, resp)
	require.Equal(t, 1, store.recordAttempts())
}

// A binding addressed to the capability surface itself would mint a second
// spelling of the module's own identity; the signer refuses it.
func TestStartModuleOperationTaskRefusesTheCapabilitySurfaceAudience(t *testing.T) {
	installModuleWorkContextAuthority(t)
	authority := business.ModuleOperationContextAuthority{
		ModuleWorkContextAuthority: business.ModuleWorkContextAuthority{
			PrincipalID: business.ModulePrincipalID("documents"),
			Tenant:      moduleWorkContextTenant,
		},
		BindingID: "surface",
		Audience:  ModuleWorkContextAudience,
		Scopes:    []business.ModuleOperationScope{{ResourceKind: "jobs", Actions: []string{"enqueue"}}},
	}

	_, _, err := WorkContextSingleton().StartModuleOperationTask(authority)
	require.ErrorIs(t, err, codefly.ErrWorkContextInvalid)

	authority.Audience = "modelservice"
	authority.Scopes = nil
	_, _, err = WorkContextSingleton().StartModuleOperationTask(authority)
	require.ErrorIs(t, err, codefly.ErrWorkContextInvalid, "a capability with no scopes is never minted")
}
