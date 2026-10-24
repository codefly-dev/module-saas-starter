package main

// A RESOURCE-CONFINED CREDENTIAL AT A DECLARED MODULE ROUTE
// (register finding SA-F-MCPAUD, round 3; gateway_module_capability.go states
// the rule).
//
// The property: such a credential is admitted at `/v1/<alias>/*` if and only if
// it arrives with an inspected capability for THAT module, in the organization
// and the session the credential itself names. Each
// refusal below is one binding broken on its own, with everything else held
// equal, so a test that passed because two things were wrong at once would not
// pass here.

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	carrier "github.com/codefly-dev/sdk-go/workcontext"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// confinedCaller is one consumer's credential and the facts a capability has to
// agree with. Returned together, because the whole rule is an agreement between
// the two and a helper that generated them independently could only test that
// nothing agrees.
type confinedCaller struct {
	bearer  string
	subject string
	org     string
	session string
}

func signConfinedCaller(t *testing.T, priv ed25519.PrivateKey, resource string) confinedCaller {
	t.Helper()
	return signConfinedCallerFor(t, priv, resource, uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String())
}

// signConfinedCallerFor is signConfinedCaller for a given person in a given
// organization, in a fresh session.
func signConfinedCallerFor(t *testing.T, priv ed25519.PrivateKey, resource, subject, org string) confinedCaller {
	t.Helper()
	caller := confinedCaller{subject: subject, org: org, session: uuid.Must(uuid.NewV7()).String()}
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   caller.subject,
			Audience:  jwt.ClaimStrings{"saas-starter", resource},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        "jti-" + uuid.Must(uuid.NewV7()).String(),
		},
		OrgID:     caller.org,
		OrgRole:   "admin",
		SessionID: caller.session,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = accessKeyID(priv.Public().(ed25519.PublicKey))
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	caller.bearer = signed
	return caller
}

// mintViewerCapability signs the capability a consumer's runtime mints for a
// composed module on a person's behalf: the module's own audience, the person as
// owner, their organization as tenant, their session as the session.
func mintViewerCapability(
	t *testing.T, kid string, priv ed25519.PrivateKey, audience, owner, tenant, session string,
) string {
	t.Helper()
	return mintCapability(t, kid, priv, edgeCapability{Audience: audience, Owner: owner, Tenant: tenant, Session: session})
}

// confinedModuleHarness wires a gateway with the edge verifier, one declared
// solution whose resource the credential names, one declared module the consumer
// reads, and an authority that has installed and granted both.
//
// `accounts` and `access` are returned because the mint hop needs them: the
// first is the upstream that receives the ceiling headers, the second signs an
// ordinary session to contrast with a confined one.
func confinedModuleHarness(t *testing.T) (
	gw *Gateway, module, accounts *fakeUpstream, caller confinedCaller,
	kid string, wcPriv, access ed25519.PrivateKey,
) {
	t.Helper()
	withPublicBase(t, testPublicBase)
	gw, accounts, _, access = newGatewayHarness(t)
	pub, wcPriv := mustEd25519(t)
	kid = "key-1"
	jwks := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)
	gw.workContext = newWorkContextVerifier(jwks.URL)
	registerSolutionUpstream(t, gw, "example")
	module = declareModule(t, gw, "documents")
	gw.solutionEntitlements = &entitledTo{ids: []string{
		fakeSolutionTarget("example"), fakeSolutionTarget("documents"),
	}}
	caller = signConfinedCaller(t, access, solutionMCPResource(testPublicBase, "example"))
	return gw, module, accounts, caller, kid, wcPriv, access
}

func moduleRequest(caller confinedCaller, capability string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	req.Header.Set("authorization", "Bearer "+caller.bearer)
	if capability != "" {
		req.Header.Set(carrier.HeaderName, capability)
	}
	return req
}

// The consumer's own work goes through, and the upstream is told what the
// credential is confined to and which installation admitted it.
func TestModuleRoute_AConfinedCredentialIsAdmittedWithThePersonsCapability(t *testing.T) {
	gw, module, _, caller, kid, wcPriv, _ := confinedModuleHarness(t)
	capability := mintViewerCapability(t, kid, wcPriv, "documents", caller.subject, caller.org, caller.session)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, moduleRequest(caller, capability))

	require.Equal(t, http.StatusOK, w.Code,
		"the consumer this credential was delivered to must be able to read the module its solution declared")
	require.NotNil(t, module.lastHeaders)
	require.Equal(t, solutionMCPResource(testPublicBase, "example"),
		module.lastHeaders.Get(resourceConfinementHeader),
		"the upstream is told which consumer this credential was delivered to")
	require.Equal(t, "install-"+fakeSolutionTarget("documents"),
		module.lastHeaders.Get(installationConfinementHeader),
		"and which installation admitted the request")
	require.Equal(t, caller.subject, module.lastHeaders.Get("X-User-Id"))
}

// Each binding, broken alone.
func TestModuleRoute_AConfinedCredentialIsRefusedWithoutAMatchingCapability(t *testing.T) {
	for name, capabilityFor := range map[string]func(c confinedCaller) (audience, owner, tenant, session string){
		"no capability at all": nil,
		"a capability for another organization": func(c confinedCaller) (string, string, string, string) {
			return "documents", c.subject, uuid.Must(uuid.NewV7()).String(), c.session
		},
		"a capability from another session": func(c confinedCaller) (string, string, string, string) {
			return "documents", c.subject, c.org, uuid.Must(uuid.NewV7()).String()
		},
		// The module's OWN identity capability verifies at this route and says
		// nothing about the person holding the credential, so it is not a
		// capability this credential may be paired with.
		"the module's own identity capability": func(c confinedCaller) (string, string, string, string) {
			return moduleCapabilitiesAudience, c.subject, c.org, c.session
		},
	} {
		t.Run(name, func(t *testing.T) {
			gw, module, _, caller, kid, wcPriv, _ := confinedModuleHarness(t)
			encoded := ""
			if capabilityFor != nil {
				audience, owner, tenant, session := capabilityFor(caller)
				encoded = mintViewerCapability(t, kid, wcPriv, audience, owner, tenant, session)
			}

			w := httptest.NewRecorder()
			gw.ServeHTTP(w, moduleRequest(caller, encoded))

			require.Equal(t, http.StatusUnauthorized, w.Code,
				"a credential confined to one consumer must not reach a module on %s", name)
			require.Nil(t, module.lastHeaders, "and nothing may reach the upstream")
		})
	}
}

// A capability for ANOTHER declared module is refused at this one's route, which
// is the A-for-B rule the edge already enforces — asserted here too, because
// listing two permitted audiences at a module route is the change that could
// have weakened it.
func TestModuleRoute_ACapabilityForAnotherModuleIsRefused(t *testing.T) {
	gw, module, _, caller, kid, wcPriv, _ := confinedModuleHarness(t)
	other := declareModule(t, gw, "ledgers")
	capability := mintViewerCapability(t, kid, wcPriv, "ledgers", caller.subject, caller.org, caller.session)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, moduleRequest(caller, capability))

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, module.lastHeaders)
	require.Nil(t, other.lastHeaders)
}

// An ORDINARY session is unchanged: it needs no capability at a module route, and
// it is stamped no confinement.
func TestModuleRoute_AnUnconfinedSessionNeedsNoCapability(t *testing.T) {
	withPublicBase(t, testPublicBase)
	gw, _, _, priv := newGatewayHarness(t)
	module := declareModule(t, gw, "documents")
	gw.solutionEntitlements = &entitledTo{ids: []string{fakeSolutionTarget("documents")}}

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/collection", nil)
	req.Header.Set("authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, module.lastHeaders.Get(resourceConfinementHeader),
		"an unbound session is confined to nothing, and the header says so by value")
}

// A deployment with NO edge verifier admits no confined credential here. The
// pairing is the only thing that admits one, and it cannot be established
// without a verifier, so the refusal is the same one round 2 shipped.
func TestModuleRoute_WithoutAnEdgeVerifierAConfinedCredentialStaysRefused(t *testing.T) {
	gw, module, _, caller, kid, wcPriv, _ := confinedModuleHarness(t)
	capability := mintViewerCapability(t, kid, wcPriv, "documents", caller.subject, caller.org, caller.session)
	gw.workContext = nil

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, moduleRequest(caller, capability))

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, module.lastHeaders)
}

// A CALLER CANNOT PICK ITS OWN CEILING. Both stamps are gateway-owned: whatever a
// caller sends under either name is replaced by what this hop resolved.
//
// Asserted here as well as in the whole-list strip test, because this is the one
// route that STAMPS them: a strip that ran before the stamp and a stamp that
// honoured a caller's value are indistinguishable from the list test's point of
// view, which only ever sees a route that stamps neither.
func TestModuleRoute_ACallerCannotAssertItsOwnConfinementOrInstallation(t *testing.T) {
	gw, module, _, caller, kid, wcPriv, _ := confinedModuleHarness(t)
	capability := mintViewerCapability(t, kid, wcPriv, "documents", caller.subject, caller.org, caller.session)

	req := moduleRequest(caller, capability)
	req.Header.Set(resourceConfinementHeader, solutionMCPResource(testPublicBase, "something-else"))
	req.Header.Set(installationConfinementHeader, "install-of-somebody-elses-organization")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, solutionMCPResource(testPublicBase, "example"),
		module.lastHeaders.Get(resourceConfinementHeader),
		"the resource is the one this credential names, not the one it asked for")
	require.Equal(t, "install-"+fakeSolutionTarget("documents"),
		module.lastHeaders.Get(installationConfinementHeader),
		"the installation is the one the authority admitted, not the one it asked for")
}

// THE OWNER IS NOT COMPARED, and that is a property of the ISSUER this edge
// relies on rather than a gap. accounts signs into a capability only its
// caller's verified session or a fresh one — never a session a request names —
// so a capability carrying this credential's session was minted by this
// credential, and a session belongs to one person. The capability below, owned by
// someone else yet carrying the bearer's session, is therefore one accounts
// cannot issue (its StartTask, StartChildSession and StartInstallationTask tests
// pin that); a hop reads no principal to act as, so it is admitted here. If the
// issuer ever signs a caller-named session again, this test is the one whose
// premise breaks.
func TestModuleRoute_TheSessionIsTheBindingAndTheOwnerIsNotRead(t *testing.T) {
	gw, module, _, caller, kid, wcPriv, _ := confinedModuleHarness(t)
	capability := mintViewerCapability(t, kid, wcPriv, "documents", uuid.Must(uuid.NewV7()).String(), caller.org, caller.session)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, moduleRequest(caller, capability))

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, module.lastHeaders)
}

// ON THE BEARER-LESS ARM a caller can assert no identity and no installation:
// everything it sent under a gateway-owned name is gone, and the arm stamps no
// installation of its own in its place.
func TestModuleRoute_TheBearerLessArmTakesNoIdentityFromTheCaller(t *testing.T) {
	gw, module, _, kid, wcPriv := operationHarness(t)
	req := onWorkloadEndpoint(publishRequest(operationCapability(t, kid, wcPriv, "documents", publisherSeal)))
	req.Header.Set(installationConfinementHeader, targetInstallation)
	req.Header.Set(resourceConfinementHeader, solutionMCPResource(testPublicBase, "something-else"))
	req.Header.Set("X-Org-Id", "55555555-5555-4555-8555-555555555555")
	req.Header.Set("X-User-Id", "attacker")
	req.Header.Set(workloadPresenterHeader, "spiffe://cluster.local/ns/other/sa/other")

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	for _, header := range []string{installationConfinementHeader, resourceConfinementHeader, "X-Org-Id", "X-User-Id", workloadPresenterHeader} {
		require.Empty(t, module.lastHeaders.Values(header), "%s is the gateway's, never the caller's", header)
	}
}

// A bearer beside an alias capability on the WORKLOAD listener is a person's
// request and takes the person path: ext_authz verifies the bearer and stamps the
// person, and the viewer admission names the target's installation.
func TestModuleRoute_ABearerOnTheWorkloadListenerTakesThePersonPath(t *testing.T) {
	gw, module, _, caller, kid, wcPriv, _ := confinedModuleHarness(t)
	gw.workloadAdmission = &admissionFake{err: errors.New("the arm must not be consulted")}
	capability := mintViewerCapability(t, kid, wcPriv, "documents", caller.subject, caller.org, caller.session)

	w := httptest.NewRecorder()
	gw.ServeHTTP(w, onWorkloadEndpoint(moduleRequest(caller, capability)))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, caller.subject, module.lastHeaders.Get("X-User-Id"), "ext_authz ran and stamped the person")
	require.Equal(t, "install-"+fakeSolutionTarget("documents"), module.lastHeaders.Get(installationConfinementHeader),
		"the viewer admission ran and named the target's installation")
	require.Equal(t, "Bearer "+caller.bearer, module.lastHeaders.Get("Authorization"))
}
