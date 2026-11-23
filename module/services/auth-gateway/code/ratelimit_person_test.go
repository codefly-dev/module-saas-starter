package main

import (
	"bytes"
	"crypto/ed25519"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// budgetLimiter is an in-process limiter with a caller budget and an
// organization ceiling small enough to spend in a test. Each allows a burst of
// a fifth above it, and never less than one: a budget of 2 admits 3.
func budgetLimiter(t *testing.T, perCaller, perOrganization int) *RateLimiter {
	t.Helper()
	limiter, err := NewRateLimiter(perCaller, WithOrganizationLimit(perOrganization), WithInProcessBackendAllowed(true))
	require.NoError(t, err)
	t.Cleanup(limiter.Stop)
	return limiter
}

// asked sends one request through the limiter as the gateway hands it over:
// with the identity headers the ext_authz check stamped, or none.
func asked(limiter *RateLimiter, organization, subject, remoteAddr string) *httptest.ResponseRecorder {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	r.RemoteAddr = remoteAddr
	if organization != "" {
		r.Header.Set("X-Org-Id", organization)
	}
	if subject != "" {
		r.Header.Set("X-User-Id", subject)
	}
	w := httptest.NewRecorder()
	limiter.Middleware(limiterFailClosed, edgeRateLimitClassUnspecified, false, next).ServeHTTP(w, r)
	return w
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buffer)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buffer
}

// One budget for a whole organization was spent by a few people using the
// product at once, and then refused all of them. Each person has their own.
func TestTwoPeopleInOneOrganizationDoNotShareABudget(t *testing.T) {
	logged := captureLog(t)
	limiter := budgetLimiter(t, 2, 1000)

	for range 3 {
		w := asked(limiter, "org-a", "jane", "198.51.100.1:1")
		require.Equal(t, http.StatusNoContent, w.Code)
		require.Equal(t, "2", w.Header().Get("X-RateLimit-Limit"))
	}
	refused := asked(limiter, "org-a", "jane", "198.51.100.1:1")
	require.Equal(t, http.StatusTooManyRequests, refused.Code)
	require.NotEmpty(t, refused.Header().Get("Retry-After"))
	require.Equal(t, "2", refused.Header().Get("X-RateLimit-Limit"))
	require.Equal(t, "0", refused.Header().Get("X-RateLimit-Remaining"))
	require.Contains(t, logged.String(), "rate limit exceeded: key=person:org-a:jane (person), limit=2/min")

	// Her colleague, from the same address, is untouched by it.
	require.Equal(t, http.StatusNoContent, asked(limiter, "org-a", "john", "198.51.100.1:1").Code)
	// And so is the same subject in another organization: the budget is a
	// person's within an organization.
	require.Equal(t, http.StatusNoContent, asked(limiter, "org-b", "jane", "198.51.100.1:1").Code)
}

// The ceiling is what everyone in an organization shares. People within their
// own budgets are still refused once their sum passes it, and the refusal says
// it was the organization's limit.
func TestTheOrganizationCeilingRefusesWhenItsPeopleTogetherExceedIt(t *testing.T) {
	logged := captureLog(t)
	limiter := budgetLimiter(t, 5, 10) // each person 6, the organization 12

	for _, person := range []string{"jane", "john", "joan"} {
		for range 4 {
			require.Equal(t, http.StatusNoContent, asked(limiter, "org-a", person, "198.51.100.1:1").Code)
		}
	}
	refused := asked(limiter, "org-a", "jack", "198.51.100.1:1")
	require.Equal(t, http.StatusTooManyRequests, refused.Code, "a fourth person, on their first request, is past the organization's ceiling")
	require.NotEmpty(t, refused.Header().Get("Retry-After"))
	require.Equal(t, "10", refused.Header().Get("X-RateLimit-Limit"))
	require.Equal(t, "0", refused.Header().Get("X-RateLimit-Remaining"))
	require.Contains(t, logged.String(), "rate limit exceeded: key=org-a (org), limit=10/min")
	require.NotContains(t, logged.String(), "(person)")

	// Another organization has its own ceiling.
	require.Equal(t, http.StatusNoContent, asked(limiter, "org-b", "jack", "198.51.100.1:1").Code)
}

// A person past their own budget is refused there, and what they go on asking
// is not counted against the organization: they cannot spend their colleagues'
// share by hammering.
func TestAPersonPastTheirBudgetSpendsNoneOfTheOrganizations(t *testing.T) {
	limiter := budgetLimiter(t, 2, 5) // a person 3, the organization 6

	for range 3 {
		require.Equal(t, http.StatusNoContent, asked(limiter, "org-a", "jane", "198.51.100.1:1").Code)
	}
	for range 20 {
		require.Equal(t, http.StatusTooManyRequests, asked(limiter, "org-a", "jane", "198.51.100.1:1").Code)
	}
	for range 3 {
		require.Equal(t, http.StatusNoContent, asked(limiter, "org-a", "john", "198.51.100.1:1").Code,
			"the organization's ceiling still has room for what was never admitted")
	}
}

// A caller with no verified identity is counted by its client address, as it
// was: no person budget, no organization ceiling.
func TestAnAnonymousCallerIsStillCountedByItsAddress(t *testing.T) {
	logged := captureLog(t)
	limiter := budgetLimiter(t, 2, 2)

	for range 3 {
		require.Equal(t, http.StatusNoContent, asked(limiter, "", "", "198.51.100.1:1234").Code)
	}
	require.Equal(t, http.StatusTooManyRequests, asked(limiter, "", "", "198.51.100.1:9999").Code)
	require.Contains(t, logged.String(), "rate limit exceeded: key=198.51.100.1 (ip), limit=2/min")
	require.Equal(t, http.StatusNoContent, asked(limiter, "", "", "198.51.100.2:1234").Code, "another address has its own budget")
	// A subject with no organization is no person of any: the address still.
	require.Equal(t, http.StatusTooManyRequests, asked(limiter, "", "jane", "198.51.100.1:1234").Code)
}

// An organization's caller that names no subject keeps the organization's
// single bucket at the caller's budget, as every caller of one did before.
func TestAnOrganizationCallerWithNoSubjectKeepsTheOrganizationBucket(t *testing.T) {
	logged := captureLog(t)
	limiter := budgetLimiter(t, 2, 1000)

	for range 3 {
		require.Equal(t, http.StatusNoContent, asked(limiter, "org-a", "", "198.51.100.1:1").Code)
	}
	require.Equal(t, http.StatusTooManyRequests, asked(limiter, "org-a", "", "198.51.100.9:1").Code)
	require.Contains(t, logged.String(), "rate limit exceeded: key=org-a (org-no-subject), limit=2/min")
	require.Equal(t, http.StatusNoContent, asked(limiter, "org-a", "jane", "198.51.100.1:1").Code, "a person of it is not in that bucket")
}

func signPersonToken(t *testing.T, priv ed25519.PrivateKey, organization, subject string) string {
	t.Helper()
	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   subject,
			Audience:  jwt.ClaimStrings{"saas-starter"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			ID:        uuid.NewString(),
		},
		OrgID:     organization,
		OrgRole:   "member",
		SessionID: uuid.Must(uuid.NewV7()).String(),
	}
	return signAccessToken(t, priv, accessKeyID(priv.Public().(ed25519.PublicKey)), claims)
}

// The bucket is chosen from the verified token, through the whole gateway: a
// caller that sends identity headers of its own is counted as who its token
// says it is, and cannot reach another person's or organization's budget —
// neither to escape its own nor to spend theirs.
func TestASpoofedIdentityHeaderCannotMoveACallerToAnotherBucket(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	gw.rateLimiter = budgetLimiter(t, 2, 1000)
	organization, jane, john := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	other := uuid.Must(uuid.NewV7()).String()

	ask := func(token string, spoof map[string]string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		for name, value := range spoof {
			r.Header.Set(name, value)
		}
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		return w.Code
	}
	janeToken := signPersonToken(t, priv, organization, jane)
	johnToken := signPersonToken(t, priv, organization, john)

	// Jane spends her budget while claiming, each time, to be someone new.
	for i := range 3 {
		require.Equal(t, http.StatusOK, ask(janeToken, map[string]string{
			"X-Org-Id": uuid.NewString(), "X-User-Id": uuid.NewString(), "Grpc-Metadata-X-Org-Id": uuid.NewString(),
		}), "request %d", i+1)
	}
	for _, spoof := range []map[string]string{
		nil,
		{"X-Org-Id": other},
		{"X-User-Id": john},
		{"X-Org-Id": other, "X-User-Id": john},
		{"X-Org-Id": ""},
	} {
		require.Equal(t, http.StatusTooManyRequests, ask(janeToken, spoof), "spoof %v", spoof)
	}

	// None of it was counted against John, whom she named: his budget is whole.
	for range 3 {
		require.Equal(t, http.StatusOK, ask(johnToken, nil))
	}
	require.Equal(t, http.StatusTooManyRequests, ask(johnToken, nil))
}

// A caller with no credential cannot name an organization to be counted under:
// it stays on its address's budget.
func TestAnAnonymousCallerCannotClaimAnOrganizationsBucket(t *testing.T) {
	gw, _, _, _ := newGatewayHarness(t)
	gw.rateLimiter = budgetLimiter(t, 2, 1000)

	ask := func(organization string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
		if organization != "" {
			r.Header.Set("X-Org-Id", organization)
			r.Header.Set("X-User-Id", uuid.NewString())
		}
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		return w.Code
	}
	for range 3 {
		require.Equal(t, http.StatusOK, ask(uuid.NewString()))
	}
	require.Equal(t, http.StatusTooManyRequests, ask(uuid.NewString()))
	require.Equal(t, http.StatusTooManyRequests, ask(""))
}

func TestConfiguredRateLimits(t *testing.T) {
	t.Cleanup(func() { require.NoError(t, codefly.LoadEnvironmentVariables()) })
	set := func(person, organization string) {
		for key, value := range map[string]string{
			"RATE_LIMIT_PER_PERSON_PER_MINUTE":       person,
			"RATE_LIMIT_PER_ORGANIZATION_PER_MINUTE": organization,
		} {
			t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__GATEWAY__"+key, "")
			t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__GATEWAY__"+key, "")
			t.Setenv(key, value)
		}
		require.NoError(t, codefly.LoadEnvironmentVariables())
	}

	set("", "")
	person, organization, err := configuredRateLimits()
	require.NoError(t, err)
	require.Equal(t, 1000, person)
	require.Equal(t, 20000, organization)

	set("300", "4500")
	person, organization, err = configuredRateLimits()
	require.NoError(t, err)
	require.Equal(t, 300, person)
	require.Equal(t, 4500, organization)

	for _, invalid := range [][2]string{{"0", ""}, {"many", ""}, {"", "-1"}, {"", "1.5"}} {
		set(invalid[0], invalid[1])
		_, _, err = configuredRateLimits()
		require.Error(t, err, "%v", invalid)
	}

	// A ceiling below one person's budget would refuse a single person at the
	// organization's limit before their own.
	set("2000", "1500")
	_, _, err = configuredRateLimits()
	require.ErrorContains(t, err, "below RATE_LIMIT_PER_PERSON_PER_MINUTE")
}
