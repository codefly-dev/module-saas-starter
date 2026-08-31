package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// testUpstreams provides standard test upstream definitions.
func testUpstreams() map[string]Upstream {
	return map[string]Upstream{
		"accounts": {Address: "accounts", Port: 5962},
	}
}

func testExtAuthz() Upstream {
	return Upstream{Address: "127.0.0.1", Port: 9000}
}

func TestPathToRegex_NoParams(t *testing.T) {
	assert.Equal(t, "^/v1/users$", pathToRegex("/v1/users"))
}

func TestPathToRegex_SingleParam(t *testing.T) {
	assert.Equal(t, "^/v1/users/[^/]+$", pathToRegex("/v1/users/{uuid}"))
}

func TestPathToRegex_MultipleParams(t *testing.T) {
	assert.Equal(t, "^/v1/organizations/[^/]+/members/[^/]+$", pathToRegex("/v1/organizations/{org_id}/members/{user_id}"))
}

func TestPathToRegex_DotInPath(t *testing.T) {
	assert.Equal(t, `^/v1/auth/\.well-known/jwks\.json$`, pathToRegex("/v1/auth/.well-known/jwks.json"))
}

func TestPathToRegex_ColonInPath(t *testing.T) {
	assert.Equal(t, "^/v1/users:findByIdentity$", pathToRegex("/v1/users:findByIdentity"))
}

func TestHasPathParams(t *testing.T) {
	assert.True(t, hasPathParams("/v1/users/{uuid}"))
	assert.True(t, hasPathParams("/v1/organizations/{org_id}/members/{user_id}"))
	assert.False(t, hasPathParams("/v1/users"))
	assert.False(t, hasPathParams("/v1/users:findByIdentity"))
}

func TestGenerateEnvoyConfig_PublicRouteDisablesExtAuthz(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "POST", Path: "/v1/auth/authenticate"},
				Connect: "/saas.accounts.v1.AuthService/Authenticate",
				Auth:    "public",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)

	// The public REST route should have ext_authz disabled.
	assert.Contains(t, output, "disabled: true")
	assert.Contains(t, output, "/v1/auth/authenticate")
	assert.Contains(t, output, "request_headers_to_remove:")
	assert.Contains(t, output, "x-codefly-gateway-token")
	assert.Contains(t, output, "x-codefly-internal-token")
}

func TestGenerateEnvoyConfig_ProtectedRouteKeepsExtAuthz(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "GET", Path: "/v1/users/self"},
				Connect: "/saas.accounts.v1.UserService/GetSelf",
				Auth:    "required",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)

	// The protected REST route should NOT have "disabled: true" in its per-filter config.
	// Parse to check more precisely.
	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(yamlBytes, &parsed))

	// The path should be present.
	assert.Contains(t, output, "/v1/users/self")

	// Count occurrences of "disabled: true" — should only appear in Connect route
	// if any (but protected routes should have none).
	lines := strings.Split(output, "\n")
	disabledCount := 0
	for _, line := range lines {
		if strings.Contains(line, "disabled: true") {
			disabledCount++
		}
	}
	// Neither the REST route nor the Connect route should have ext_authz disabled.
	assert.Equal(t, 0, disabledCount, "protected routes should not disable ext_authz")
}

func TestGenerateEnvoyConfig_MFAPendingRouteKeepsExtAuthz(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "POST", Path: "/v1/mfa/totp/verify"},
				Connect: "/saas.accounts.v1.MFAService/VerifyTOTP",
				Auth:    "mfa_pending",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)
	// mfa_pending is NOT public — ext_authz should remain enabled.
	assert.NotContains(t, output, "disabled: true")
}

func TestGenerateEnvoyConfig_PathParamsBecomeSafeRegex(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "GET", Path: "/v1/users/{uuid}"},
				Connect: "/saas.accounts.v1.UserService/GetUser",
				Auth:    "required",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)

	// Should use safe_regex, not path.
	assert.Contains(t, output, "safe_regex")
	assert.Contains(t, output, `regex: ^/v1/users/[^/]+$`)
	// The Connect route should be exact match (no regex).
	assert.Contains(t, output, "/saas.accounts.v1.UserService/GetUser")
}

func TestGenerateEnvoyConfig_ConnectRoutesAreExactMatch(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "POST", Path: "/v1/auth/authenticate"},
				Connect: "/saas.accounts.v1.AuthService/Authenticate",
				Auth:    "public",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	// Parse YAML to inspect Connect route.
	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(yamlBytes, &parsed))

	output := string(yamlBytes)
	// Connect routes use exact path match.
	assert.Contains(t, output, "path: /saas.accounts.v1.AuthService/Authenticate")
}

func TestGenerateEnvoyConfig_SelfRoutesAreDirectResponse(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "self",
				Rest:    &RestPath{Method: "GET", Path: "/health"},
				Auth:    "public",
			},
			{
				Service: "self",
				Rest:    &RestPath{Method: "GET", Path: "/healthz"},
				Auth:    "public",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)
	assert.Contains(t, output, "direct_response")
	assert.Contains(t, output, "inline_string: ok")
	assert.Contains(t, output, "/health")
	assert.Contains(t, output, "/healthz")
	// Self routes should also disable ext_authz.
	assert.Contains(t, output, "disabled: true")
}

func TestGenerateEnvoyConfig_AllRoutesFromFile(t *testing.T) {
	// Load the actual route files from the routing directory.
	ctx := context.Background()
	entries, err := LoadAllRESTRoutes(ctx, DefaultRoutingDir())
	require.NoError(t, err)
	config := ToEnvoyConfig(entries)

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)
	require.NotEmpty(t, yamlBytes)

	output := string(yamlBytes)

	// Count the routes in the generated config.
	// Each route entry in YAML starts with "- match:" under the routes key.
	matchCount := strings.Count(output, "- match:")

	// We expect at least one Envoy route per route entry in the config.
	// Routes with both REST and Connect produce 2 Envoy routes each.
	// Routes with only REST (billing webhook and self routes) produce 1.
	// Count REST routes and Connect routes separately.
	expectedREST := 0
	expectedConnect := 0
	for _, r := range config.Routes {
		if r.Rest != nil {
			expectedREST++
		}
		if r.Connect != "" {
			expectedConnect++
		}
	}

	expectedTotal := expectedREST + expectedConnect
	assert.Equal(t, expectedTotal, matchCount,
		"expected %d Envoy route entries (REST=%d + Connect=%d), got %d",
		expectedTotal, expectedREST, expectedConnect, matchCount)
}

func TestGenerateEnvoyConfig_ValidYAML(t *testing.T) {
	ctx := context.Background()
	entries, err := LoadAllRESTRoutes(ctx, DefaultRoutingDir())
	require.NoError(t, err)
	config := ToEnvoyConfig(entries)

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	// Must parse as valid YAML.
	var parsed map[string]interface{}
	require.NoError(t, yaml.Unmarshal(yamlBytes, &parsed))

	// Must have static_resources.
	sr, ok := parsed["static_resources"]
	require.True(t, ok, "missing static_resources")
	srMap, ok := sr.(map[string]interface{})
	require.True(t, ok)

	// Must have listeners.
	listeners, ok := srMap["listeners"]
	require.True(t, ok, "missing listeners")
	listenerList, ok := listeners.([]interface{})
	require.True(t, ok)
	assert.Len(t, listenerList, 1)

	// Must have clusters.
	clusters, ok := srMap["clusters"]
	require.True(t, ok, "missing clusters")
	clusterList, ok := clusters.([]interface{})
	require.True(t, ok)
	// At least ext_authz_cluster + api_rest.
	assert.GreaterOrEqual(t, len(clusterList), 2)
}

func TestGenerateEnvoyConfig_ExtAuthzClusterIsGRPC(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "GET", Path: "/v1/version"},
				Auth:    "public",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)
	// The ext_authz cluster should have HTTP/2 protocol options.
	assert.Contains(t, output, "ext_authz_cluster")
	assert.Contains(t, output, "envoy.extensions.upstreams.http.v3.HttpProtocolOptions")
	assert.Contains(t, output, "http2_protocol_options")
}

func TestGenerateEnvoyConfig_NilConfig(t *testing.T) {
	_, err := GenerateEnvoyConfig(nil, testUpstreams(), nil, testExtAuthz(), 8080)
	assert.Error(t, err)
}

func TestGenerateEnvoyConfig_SeparateConnectUpstreams(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "POST", Path: "/v1/auth/authenticate"},
				Connect: "/saas.accounts.v1.AuthService/Authenticate",
				Auth:    "public",
			},
		},
	}

	connectUpstreams := map[string]Upstream{
		"accounts": {Address: "accounts-connect", Port: 5963},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), connectUpstreams, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)
	// Should have separate accounts_rest and accounts_connect clusters.
	assert.Contains(t, output, "accounts_rest")
	assert.Contains(t, output, "accounts_connect")
	assert.Contains(t, output, "accounts-connect") // connect upstream hostname
}

func TestGenerateEnvoyConfig_ListenPort(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "GET", Path: "/v1/version"},
				Auth:    "public",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 9090)
	require.NoError(t, err)

	output := string(yamlBytes)
	assert.Contains(t, output, "port_value: 9090")
}

func TestRegexEscapeLiteral(t *testing.T) {
	assert.Equal(t, `hello\.world`, regexEscapeLiteral("hello.world"))
	assert.Equal(t, `noop`, regexEscapeLiteral("noop"))
	assert.Equal(t, `a\+b`, regexEscapeLiteral("a+b"))
}

func TestGenerateEnvoyConfig_NoUnmatchedRoutes(t *testing.T) {
	// Verify that with an empty config, no routes are generated
	// (meaning Envoy will 404 everything by default).
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)
	// Should have no "- match:" entries.
	assert.Equal(t, 0, strings.Count(output, "- match:"),
		"empty config should produce zero routes")
}

func TestGenerateEnvoyConfig_ComplexPathParams(t *testing.T) {
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "DELETE", Path: "/v1/organizations/{org_id}/members/{user_id}"},
				Connect: "/saas.accounts.v1.OrganizationService/RemoveMember",
				Auth:    "required",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)
	// REST route should use regex.
	assert.Contains(t, output, "safe_regex")
	assert.Contains(t, output, `^/v1/organizations/[^/]+/members/[^/]+$`)
	// Method should be DELETE.
	assert.Contains(t, output, "exact_match: DELETE")
}

func TestGenerateEnvoyConfig_CustomActionPaths(t *testing.T) {
	// Paths with :action syntax (e.g., /v1/users:findByIdentity).
	config := &EnvoyRouteConfig{
		Routes: []EnvoyRouteEntry{
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "GET", Path: "/v1/users:findByIdentity"},
				Connect: "/saas.accounts.v1.UserService/FindUserByIdentity",
				Auth:    "required",
			},
			{
				Service: "accounts",
				Rest:    &RestPath{Method: "POST", Path: "/v1/platform/users/{user_id}:suspend"},
				Connect: "/saas.accounts.v1.PlatformAdminService/SuspendUser",
				Auth:    "required",
			},
		},
	}

	yamlBytes, err := GenerateEnvoyConfig(config, testUpstreams(), nil, testExtAuthz(), 8080)
	require.NoError(t, err)

	output := string(yamlBytes)
	// Static custom action path should be exact match.
	assert.Contains(t, output, "path: /v1/users:findByIdentity")
	// Parameterized custom action path should be regex.
	assert.Contains(t, output, `^/v1/platform/users/[^/]+:suspend$`)
}

func TestGenerateEnvoyConfig_LegacyConnectAliasRewritesToV1(t *testing.T) {
	entries := []*RouteEntry{{
		Service:      "accounts_connect",
		Method:       http.MethodPost,
		Path:         "/customers.UserService/GetSelf",
		UpstreamPath: "/saas.accounts.v1.UserService/GetSelf",
		Protected:    true,
	}}
	config := ToEnvoyConfig(entries)
	require.Len(t, config.Routes, 1)
	require.Equal(t, "/customers.UserService/GetSelf", config.Routes[0].Connect)

	yamlBytes, err := GenerateEnvoyConfig(
		config,
		testUpstreams(),
		map[string]Upstream{"accounts": {Address: "accounts", Port: 5963}},
		testExtAuthz(),
		8080,
	)
	require.NoError(t, err)
	output := string(yamlBytes)
	assert.Contains(t, output, "path: /customers.UserService/GetSelf")
	assert.Contains(t, output, "regex_rewrite:")
	assert.Contains(t, output, `regex: ^/customers\.UserService/GetSelf$`)
	assert.Contains(t, output, "substitution: /saas.accounts.v1.UserService/GetSelf")
}
