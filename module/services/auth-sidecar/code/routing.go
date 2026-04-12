package main

// Route configuration loader for the auth-sidecar gateway.
//
// Loads a static YAML file that explicitly whitelists every HTTP endpoint
// the gateway is allowed to forward. Any request not matching an entry in
// this file is rejected with 404 — zero prefix matching, zero implicit
// exposure.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// RouteConfig is the top-level structure of routes.codefly.yaml.
type RouteConfig struct {
	Routes []RouteEntry `yaml:"routes"`
}

// RouteEntry defines a single whitelisted endpoint.
type RouteEntry struct {
	Service string    `yaml:"service"`
	Rest    *RestPath `yaml:"rest"`
	Connect string    `yaml:"connect"`
	Auth    string    `yaml:"auth"` // "required", "public", "mfa_pending"
}

// RestPath is the HTTP method + path for a REST endpoint.
type RestPath struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
}

// RouteMatcher provides fast lookups against the loaded route config.
// REST routes are keyed by "METHOD /path/pattern" and support path
// parameters ({param} segments). Connect routes are exact-match.
type RouteMatcher struct {
	// restRoutes stores routes indexed by "METHOD" for linear scan
	// within a method group. Each entry has pre-split path segments
	// for parameter matching.
	restRoutes map[string][]restRoute

	// connectRoutes maps exact Connect RPC paths to their entry.
	connectRoutes map[string]*RouteEntry
}

// restRoute is an internal representation with pre-split path segments.
type restRoute struct {
	segments []string // e.g. ["", "v1", "users", "{uuid}"]
	entry    *RouteEntry
}

// LoadRouteConfig reads and parses the route config YAML file.
// It looks for the file at the given path; if empty, it defaults to
// ../routing/routes.codefly.yaml relative to the source file (for dev)
// or relative to the working directory.
func LoadRouteConfig(path string) (*RouteConfig, error) {
	if path == "" {
		// Default: relative to the code directory
		path = defaultRouteConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("routing: cannot read config %s: %w", path, err)
	}

	var cfg RouteConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("routing: cannot parse config %s: %w", path, err)
	}

	// Validate entries.
	for i, r := range cfg.Routes {
		if r.Service == "" {
			return nil, fmt.Errorf("routing: route %d missing service", i)
		}
		if r.Auth == "" {
			return nil, fmt.Errorf("routing: route %d missing auth", i)
		}
		if r.Auth != "required" && r.Auth != "public" && r.Auth != "mfa_pending" {
			return nil, fmt.Errorf("routing: route %d invalid auth %q", i, r.Auth)
		}
		if r.Rest == nil && r.Connect == "" {
			return nil, fmt.Errorf("routing: route %d has neither rest nor connect path", i)
		}
	}

	log.Printf("routing: loaded %d routes from %s", len(cfg.Routes), path)
	return &cfg, nil
}

// defaultRouteConfigPath returns the default path to routes.codefly.yaml.
func defaultRouteConfigPath() string {
	// Try relative to the source file first (works in dev/test).
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Dir(filename)
		candidate := filepath.Join(dir, "..", "routing", "routes.codefly.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Fallback: relative to working directory.
	return filepath.Join("..", "routing", "routes.codefly.yaml")
}

// NewRouteMatcher builds a RouteMatcher from a loaded RouteConfig.
func NewRouteMatcher(cfg *RouteConfig) *RouteMatcher {
	m := &RouteMatcher{
		restRoutes:    make(map[string][]restRoute),
		connectRoutes: make(map[string]*RouteEntry),
	}

	for i := range cfg.Routes {
		entry := &cfg.Routes[i]

		if entry.Rest != nil {
			method := strings.ToUpper(entry.Rest.Method)
			segments := strings.Split(entry.Rest.Path, "/")
			m.restRoutes[method] = append(m.restRoutes[method], restRoute{
				segments: segments,
				entry:    entry,
			})
		}

		if entry.Connect != "" {
			m.connectRoutes[entry.Connect] = entry
		}
	}

	return m
}

// MatchREST looks up a REST route by HTTP method and path.
// Returns nil if no match. Supports path parameters: a config path
// segment like {uuid} matches any value in the request path.
func (m *RouteMatcher) MatchREST(method, path string) *RouteEntry {
	method = strings.ToUpper(method)
	candidates, ok := m.restRoutes[method]
	if !ok {
		return nil
	}

	reqSegments := strings.Split(path, "/")

	for _, c := range candidates {
		if matchSegments(c.segments, reqSegments) {
			return c.entry
		}
	}
	return nil
}

// MatchConnect looks up a Connect RPC route by exact path.
func (m *RouteMatcher) MatchConnect(path string) *RouteEntry {
	return m.connectRoutes[path]
}

// Match tries Connect first (if path looks like one), then REST.
func (m *RouteMatcher) Match(method, path string) *RouteEntry {
	// Connect paths start with a slash followed by a package name containing a dot.
	if isConnectPath(path) {
		if entry := m.MatchConnect(path); entry != nil {
			return entry
		}
	}
	return m.MatchREST(method, path)
}

// isConnectPath returns true if the path looks like a Connect RPC path
// (e.g., /customers.AuthService/Authenticate).
func isConnectPath(path string) bool {
	if len(path) < 2 || path[0] != '/' {
		return false
	}
	// Connect paths: /package.Service/Method — contain a dot before the second slash.
	rest := path[1:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return false
	}
	return strings.Contains(rest[:slashIdx], ".")
}

// matchSegments compares a pattern (from the config) against a request
// path, both split by "/". Segments wrapped in {} match any value.
func matchSegments(pattern, request []string) bool {
	if len(pattern) != len(request) {
		return false
	}
	for i := range pattern {
		if isParam(pattern[i]) {
			// {param} matches any non-empty segment.
			if request[i] == "" {
				return false
			}
			continue
		}
		if pattern[i] != request[i] {
			return false
		}
	}
	return true
}

// isParam returns true if the segment is a path parameter like {uuid}.
func isParam(segment string) bool {
	return len(segment) >= 3 && segment[0] == '{' && segment[len(segment)-1] == '}'
}
