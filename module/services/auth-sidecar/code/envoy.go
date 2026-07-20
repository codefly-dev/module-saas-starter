package main

// Envoy configuration generator.
//
// Reads the parsed RouteConfig (from routes.codefly.yaml) and produces a
// complete Envoy static bootstrap configuration. The generated config is
// identical for dev and production — same file, same behavior.
//
// Key design decisions:
//   - NO wildcard routes. Every path is explicit.
//   - Routes with auth "public" disable ext_authz via per-route config.
//   - Routes with auth "required" or "mfa_pending" leave ext_authz enabled (default).
//   - Path parameters ({param}) become safe_regex matchers ([^/]+).
//   - Connect RPC paths are always exact-match POST routes.
//   - Unmatched paths get Envoy's default 404.

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Upstream describes a backend service that Envoy should proxy to.
type Upstream struct {
	Address string // hostname or IP
	Port    uint32
}

// envoyConfig is the top-level Envoy bootstrap structure (static_resources only).
type envoyConfig struct {
	StaticResources envoyStaticResources `yaml:"static_resources"`
}

type envoyStaticResources struct {
	Listeners []envoyListener `yaml:"listeners"`
	Clusters  []envoyCluster  `yaml:"clusters"`
}

// --- Listener types ---

type envoyListener struct {
	Name         string             `yaml:"name"`
	Address      envoyAddress       `yaml:"address"`
	FilterChains []envoyFilterChain `yaml:"filter_chains"`
}

type envoyAddress struct {
	SocketAddress envoySocketAddress `yaml:"socket_address"`
}

type envoySocketAddress struct {
	Address   string `yaml:"address"`
	PortValue uint32 `yaml:"port_value"`
}

type envoyFilterChain struct {
	Filters []envoyFilter `yaml:"filters"`
}

type envoyFilter struct {
	Name        string      `yaml:"name"`
	TypedConfig interface{} `yaml:"typed_config"`
}

// envoyHCM is the HttpConnectionManager typed_config.
type envoyHCM struct {
	Type        string            `yaml:"@type"`
	StatPrefix  string            `yaml:"stat_prefix"`
	HTTPFilters []envoyHTTPFilter `yaml:"http_filters"`
	RouteConfig envoyRouteConfig  `yaml:"route_config"`
}

type envoyHTTPFilter struct {
	Name        string      `yaml:"name"`
	TypedConfig interface{} `yaml:"typed_config,omitempty"`
}

// envoyExtAuthzConfig is the ext_authz filter typed_config.
type envoyExtAuthzConfig struct {
	Type                string            `yaml:"@type"`
	GRPCService         envoyGRPCService  `yaml:"grpc_service"`
	TransportAPIVersion string            `yaml:"transport_api_version"`
	WithRequestBody     *envoyRequestBody `yaml:"with_request_body,omitempty"`
	FailureModeAllow    bool              `yaml:"failure_mode_allow"`
}

type envoyGRPCService struct {
	EnvoyGRPC envoyEnvoyGRPC `yaml:"envoy_grpc"`
	Timeout   string         `yaml:"timeout"`
}

type envoyEnvoyGRPC struct {
	ClusterName string `yaml:"cluster_name"`
}

type envoyRequestBody struct {
	MaxRequestBytes     uint32 `yaml:"max_request_bytes"`
	AllowPartialMessage bool   `yaml:"allow_partial_message"`
}

type envoyRouteConfig struct {
	Name         string             `yaml:"name"`
	VirtualHosts []envoyVirtualHost `yaml:"virtual_hosts"`
}

type envoyVirtualHost struct {
	Name    string       `yaml:"name"`
	Domains []string     `yaml:"domains"`
	Routes  []envoyRoute `yaml:"routes"`
}

type envoyRoute struct {
	Match                envoyMatch             `yaml:"match"`
	Route                *envoyRouteAction      `yaml:"route,omitempty"`
	DirectResponse       *envoyDirectResponse   `yaml:"direct_response,omitempty"`
	TypedPerFilterConfig map[string]interface{} `yaml:"typed_per_filter_config,omitempty"`
}

type envoyMatch struct {
	Path      string             `yaml:"path,omitempty"`
	SafeRegex *envoySafeRegex    `yaml:"safe_regex,omitempty"`
	Prefix    string             `yaml:"prefix,omitempty"`
	Headers   []envoyHeaderMatch `yaml:"headers,omitempty"`
}

type envoySafeRegex struct {
	Regex string `yaml:"regex"`
}

type envoyHeaderMatch struct {
	Name       string `yaml:"name"`
	ExactMatch string `yaml:"exact_match"`
}

type envoyRouteAction struct {
	Cluster                string             `yaml:"cluster"`
	RequestHeadersToRemove []string           `yaml:"request_headers_to_remove,omitempty"`
	RegexRewrite           *envoyRegexRewrite `yaml:"regex_rewrite,omitempty"`
}

type envoyRegexRewrite struct {
	Pattern      envoyRegexPattern `yaml:"pattern"`
	Substitution string            `yaml:"substitution"`
}

type envoyRegexPattern struct {
	GoogleRE2 map[string]interface{} `yaml:"google_re2"`
	Regex     string                 `yaml:"regex"`
}

type envoyDirectResponse struct {
	Status uint32           `yaml:"status"`
	Body   *envoyDirectBody `yaml:"body,omitempty"`
}

type envoyDirectBody struct {
	InlineString string `yaml:"inline_string"`
}

// envoyExtAuthzPerRoute is the per-route ext_authz override.
type envoyExtAuthzPerRoute struct {
	Type     string `yaml:"@type"`
	Disabled bool   `yaml:"disabled,omitempty"`
}

// --- Cluster types ---

type envoyCluster struct {
	Name           string              `yaml:"name"`
	Type           string              `yaml:"type"`
	ConnectTimeout string              `yaml:"connect_timeout"`
	LBPolicy       string              `yaml:"lb_policy"`
	LoadAssignment envoyLoadAssignment `yaml:"load_assignment"`
	HTTP2Options   *envoyHTTP2Options  `yaml:"typed_extension_protocol_options,omitempty"`
}

type envoyLoadAssignment struct {
	ClusterName string            `yaml:"cluster_name"`
	Endpoints   []envoyLBEndpoint `yaml:"endpoints"`
}

type envoyLBEndpoint struct {
	LBEndpoints []envoyEndpointEntry `yaml:"lb_endpoints"`
}

type envoyEndpointEntry struct {
	Endpoint envoyEndpointAddr `yaml:"endpoint"`
}

type envoyEndpointAddr struct {
	Address envoyAddress `yaml:"address"`
}

type envoyHTTP2Options struct {
	EnvoyGRPC envoyHTTP2Proto `yaml:"envoy.extensions.upstreams.http.v3.HttpProtocolOptions"`
}

type envoyHTTP2Proto struct {
	Type               string            `yaml:"@type"`
	ExplicitHTTPConfig envoyExplicitHTTP `yaml:"explicit_http_config"`
}

type envoyExplicitHTTP struct {
	HTTP2ProtocolOptions map[string]interface{} `yaml:"http2_protocol_options"`
}

// pathParamRegex matches {param} segments in REST paths.
var pathParamRegex = regexp.MustCompile(`\{[^}]+\}`)

// hasPathParams returns true if the path contains {param} segments.
func hasPathParams(path string) bool {
	return pathParamRegex.MatchString(path)
}

// pathToRegex converts a REST path with {param} segments into an Envoy
// safe_regex pattern. Each {param} becomes [^/]+. Segments that mix a
// param with literal text (e.g., "{user_id}:suspend") are handled by
// replacing each {param} occurrence within the segment.
func pathToRegex(path string) string {
	segments := strings.Split(path, "/")
	var result []string
	for _, seg := range segments {
		if isParam(seg) {
			// Pure parameter segment.
			result = append(result, "[^/]+")
		} else if pathParamRegex.MatchString(seg) {
			// Mixed segment: contains {param} plus literal text.
			// Replace each {param} with [^/]+ and escape the rest.
			converted := pathParamRegex.ReplaceAllStringFunc(seg, func(_ string) string {
				return "\x00PARAM\x00"
			})
			escaped := regexEscapeLiteral(converted)
			escaped = strings.ReplaceAll(escaped, "\x00PARAM\x00", "[^/]+")
			result = append(result, escaped)
		} else {
			result = append(result, regexEscapeLiteral(seg))
		}
	}
	return "^" + strings.Join(result, "/") + "$"
}

// regexEscapeLiteral escapes regex metacharacters in a literal string.
func regexEscapeLiteral(s string) string {
	// Characters that need escaping in regex: . * + ? ^ $ { } [ ] ( ) | \
	// In URL path segments, the common ones are . and :
	var b strings.Builder
	for _, c := range s {
		switch c {
		case '.', '*', '+', '?', '^', '$', '{', '}', '[', ']', '(', ')', '|', '\\':
			b.WriteRune('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// clusterNameForService returns the Envoy cluster name for a given service
// and protocol. The "self" service is handled separately (direct_response).
func clusterNameForService(service string, isConnect bool) string {
	if isConnect {
		return service + "_connect"
	}
	return service + "_rest"
}

// GenerateEnvoyConfig produces a complete Envoy static bootstrap YAML from
// the parsed route config and upstream definitions.
//
// Parameters:
//   - config: parsed RouteConfig from routes.codefly.yaml
//   - upstreams: map of service name to Upstream (address + port) for REST endpoints
//   - connectUpstreams: map of service name to Upstream for Connect endpoints (may be nil)
//   - extAuthz: Upstream for the ext_authz gRPC cluster (the sidecar itself)
//   - listenPort: the port for the main listener
func GenerateEnvoyConfig(
	config *EnvoyRouteConfig,
	upstreams map[string]Upstream,
	connectUpstreams map[string]Upstream,
	extAuthz Upstream,
	listenPort uint32,
) ([]byte, error) {
	if config == nil {
		return nil, fmt.Errorf("envoy: route config is nil")
	}

	// Build routes.
	var routes []envoyRoute
	// Track which clusters we actually need.
	neededClusters := map[string]bool{}

	for _, entry := range config.Routes {
		// "self" routes become direct_response (health checks).
		if entry.Service == "self" {
			if entry.Rest != nil {
				r := buildSelfRoute(entry)
				routes = append(routes, r)
			}
			continue
		}

		// REST route.
		if entry.Rest != nil {
			clusterName := clusterNameForService(entry.Service, false)
			neededClusters[clusterName] = true
			r := buildRESTRoute(entry, clusterName)
			routes = append(routes, r)
		}

		// Connect route (always POST, exact path match).
		if entry.Connect != "" {
			clusterName := clusterNameForService(entry.Service, true)
			// Fall back to REST upstream if no separate Connect upstream.
			if connectUpstreams == nil {
				clusterName = clusterNameForService(entry.Service, false)
			} else if _, ok := connectUpstreams[entry.Service]; !ok {
				clusterName = clusterNameForService(entry.Service, false)
			}
			neededClusters[clusterName] = true
			r := buildConnectRoute(entry, clusterName)
			routes = append(routes, r)
		}
	}

	// Build clusters.
	var clusters []envoyCluster

	// ext_authz cluster (gRPC, needs HTTP/2).
	clusters = append(clusters, buildGRPCCluster("ext_authz_cluster", extAuthz))

	// REST upstream clusters.
	for name, upstream := range upstreams {
		cName := name + "_rest"
		if neededClusters[cName] {
			clusters = append(clusters, buildHTTPCluster(cName, upstream))
		}
	}

	// Connect upstream clusters (if separate).
	if connectUpstreams != nil {
		for name, upstream := range connectUpstreams {
			cName := name + "_connect"
			if neededClusters[cName] {
				clusters = append(clusters, buildHTTPCluster(cName, upstream))
			}
		}
	}

	// Assemble the full config.
	cfg := envoyConfig{
		StaticResources: envoyStaticResources{
			Listeners: []envoyListener{
				{
					Name: "main",
					Address: envoyAddress{
						SocketAddress: envoySocketAddress{
							Address:   "0.0.0.0",
							PortValue: listenPort,
						},
					},
					FilterChains: []envoyFilterChain{
						{
							Filters: []envoyFilter{
								{
									Name: "envoy.filters.network.http_connection_manager",
									TypedConfig: envoyHCM{
										Type:       "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
										StatPrefix: "ingress_http",
										HTTPFilters: []envoyHTTPFilter{
											{
												Name: "envoy.filters.http.ext_authz",
												TypedConfig: envoyExtAuthzConfig{
													Type: "type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz",
													GRPCService: envoyGRPCService{
														EnvoyGRPC: envoyEnvoyGRPC{
															ClusterName: "ext_authz_cluster",
														},
														Timeout: "5s",
													},
													TransportAPIVersion: "V3",
													FailureModeAllow:    false,
												},
											},
											{
												Name: "envoy.filters.http.router",
												TypedConfig: map[string]string{
													"@type": "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router",
												},
											},
										},
										RouteConfig: envoyRouteConfig{
											Name: "local_route",
											VirtualHosts: []envoyVirtualHost{
												{
													Name:    "gateway",
													Domains: []string{"*"},
													Routes:  routes,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			Clusters: clusters,
		},
	}

	return yaml.Marshal(&cfg)
}

// buildRESTRoute creates an Envoy route entry for a REST endpoint.
func buildRESTRoute(entry EnvoyRouteEntry, cluster string) envoyRoute {
	r := envoyRoute{
		Route: &envoyRouteAction{Cluster: cluster},
	}

	method := strings.ToUpper(entry.Rest.Method)
	path := entry.Rest.Path

	// Match configuration.
	if hasPathParams(path) {
		r.Match = envoyMatch{
			SafeRegex: &envoySafeRegex{Regex: pathToRegex(path)},
			Headers: []envoyHeaderMatch{
				{Name: ":method", ExactMatch: method},
			},
		}
	} else {
		r.Match = envoyMatch{
			Path: path,
			Headers: []envoyHeaderMatch{
				{Name: ":method", ExactMatch: method},
			},
		}
	}

	// Per-route ext_authz config: disable for public routes.
	if entry.Auth == "public" {
		r.Route.RequestHeadersToRemove = append([]string(nil), untrustedAuthHeaders...)
		r.TypedPerFilterConfig = map[string]interface{}{
			"envoy.filters.http.ext_authz": envoyExtAuthzPerRoute{
				Type:     "type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthzPerRoute",
				Disabled: true,
			},
		}
	}

	return r
}

// buildConnectRoute creates an Envoy route entry for a Connect RPC endpoint.
// Connect paths are always POST and always exact-match.
func buildConnectRoute(entry EnvoyRouteEntry, cluster string) envoyRoute {
	r := envoyRoute{
		Match: envoyMatch{
			Path: entry.Connect,
			Headers: []envoyHeaderMatch{
				{Name: ":method", ExactMatch: "POST"},
			},
		},
		Route: &envoyRouteAction{Cluster: cluster},
	}
	if entry.Rewrite != "" {
		r.Route.RegexRewrite = &envoyRegexRewrite{
			Pattern: envoyRegexPattern{
				GoogleRE2: map[string]interface{}{},
				Regex:     "^" + regexp.QuoteMeta(entry.Connect) + "$",
			},
			Substitution: entry.Rewrite,
		}
	}

	// Per-route ext_authz config: disable for public routes.
	if entry.Auth == "public" {
		r.Route.RequestHeadersToRemove = append([]string(nil), untrustedAuthHeaders...)
		r.TypedPerFilterConfig = map[string]interface{}{
			"envoy.filters.http.ext_authz": envoyExtAuthzPerRoute{
				Type:     "type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthzPerRoute",
				Disabled: true,
			},
		}
	}

	return r
}

// buildSelfRoute creates a direct_response route for health checks.
func buildSelfRoute(entry EnvoyRouteEntry) envoyRoute {
	return envoyRoute{
		Match: envoyMatch{
			Path: entry.Rest.Path,
			Headers: []envoyHeaderMatch{
				{Name: ":method", ExactMatch: strings.ToUpper(entry.Rest.Method)},
			},
		},
		DirectResponse: &envoyDirectResponse{
			Status: 200,
			Body:   &envoyDirectBody{InlineString: "ok"},
		},
		TypedPerFilterConfig: map[string]interface{}{
			"envoy.filters.http.ext_authz": envoyExtAuthzPerRoute{
				Type:     "type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthzPerRoute",
				Disabled: true,
			},
		},
	}
}

// buildHTTPCluster creates an Envoy cluster for an HTTP/1.1 upstream.
func buildHTTPCluster(name string, upstream Upstream) envoyCluster {
	return envoyCluster{
		Name:           name,
		Type:           "STRICT_DNS",
		ConnectTimeout: "5s",
		LBPolicy:       "ROUND_ROBIN",
		LoadAssignment: envoyLoadAssignment{
			ClusterName: name,
			Endpoints: []envoyLBEndpoint{
				{
					LBEndpoints: []envoyEndpointEntry{
						{
							Endpoint: envoyEndpointAddr{
								Address: envoyAddress{
									SocketAddress: envoySocketAddress{
										Address:   upstream.Address,
										PortValue: upstream.Port,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// buildGRPCCluster creates an Envoy cluster for a gRPC upstream (HTTP/2).
func buildGRPCCluster(name string, upstream Upstream) envoyCluster {
	c := buildHTTPCluster(name, upstream)
	c.HTTP2Options = &envoyHTTP2Options{
		EnvoyGRPC: envoyHTTP2Proto{
			Type: "type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
			ExplicitHTTPConfig: envoyExplicitHTTP{
				HTTP2ProtocolOptions: map[string]interface{}{},
			},
		},
	}
	return c
}
