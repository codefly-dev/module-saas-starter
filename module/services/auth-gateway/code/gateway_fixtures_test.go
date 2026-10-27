package main

// Fixtures shared by the gateway's tests: a declared module record in the
// registry (a module may still contribute a page, and the registry projection
// names its kind), a captured log, and a resource-confined caller.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log"
	"net/http/httptest"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// declareModule wires a declared MODULE record into the registry behind gw and
// returns the upstream's capture struct.
func declareModule(t *testing.T, gw *Gateway, alias string) *fakeUpstream {
	t.Helper()
	fake := &fakeUpstream{body: "module-response"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.revision++
	registry.records[alias] = &accountsv1.SolutionRegistration{
		SolutionId: alias,
		Publisher:  "solution:" + alias,
		Revision:   registry.revision,
		Declared: &accountsv1.SolutionDeclaredBinding{
			BindingId:  "acme.test." + alias,
			Generation: 1,
			Release:    "acme/" + alias + "@1.0.0",
			TargetId:   fakeSolutionTarget(alias),
			Kind:       accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE,
		},
		// NO frontend block, which is the real shape: a module has no browser
		// remote and nothing loads a manifest for it. Giving the fake one would
		// hide the rule that makes modules routable at all.
	}
	registry.route(alias, srv.URL)
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))
	return fake
}

// captureLog sends the standard logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

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
