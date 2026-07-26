package main

// Route configuration loader for the auth-sidecar gateway.
//
// Descriptor REST routes come from the generated saas.rest.surface.v1 catalog.
// The folder-based *.rest.codefly.yaml source contains only explicit
// non-protobuf extensions. Any request not matching an entry is rejected with
// 404.
//
// This is the same pattern used by the KrakenD gateway agent in codefly,
// reusing core's ExtendedRestRouteLoader[T].

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/codefly-dev/core/resources"
)

// RouteExtension carries the per-route auth configuration in the
// extension field of *.rest.codefly.yaml files.
type RouteExtension struct {
	Exposed   bool `yaml:"exposed"`
	Protected bool `yaml:"protected"`
}

// RouteEntry is the internal representation used by the gateway.
type RouteEntry struct {
	Service                     string             // upstream service name (from directory path)
	Method                      string             // HTTP method
	Path                        string             // public HTTP path (may contain {param} segments)
	UpstreamPath                string             // optional canonical path used for a compatibility alias
	Procedure                   string             // canonical descriptor procedure when this is a protobuf route
	Protected                   bool               // true = auth required, false = public
	RateLimitClass              edgeRateLimitClass // descriptor-selected limiter budget class
	RateLimitBackendFailClosed  bool               // preserve the security budget when the limiter backend fails
	AuthenticationFactorAttempt bool               // use the dedicated per-client-IP login-factor budget
	PolicySHA256                string             // stable fingerprint of the complete method policy
}

// RouteMatcher provides fast lookups against the loaded route config.
type RouteMatcher struct {
	restRoutes    map[string][]restRoute
	connectRoutes map[string]*RouteEntry
}

type restRoute struct {
	segments []string
	entry    *RouteEntry
}

// LoadRESTExtensionsFromDir loads the explicit non-protobuf REST extensions.
// Descriptor-owned routes and disabled entries are rejected so this source
// cannot regress into a shadow route inventory.
func LoadRESTExtensionsFromDir(ctx context.Context, dir string) ([]*RouteEntry, error) {
	loader, err := resources.NewExtendedRestRouteLoader[RouteExtension](ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("routing: cannot create loader for %s: %w", dir, err)
	}
	if err := loader.Load(ctx); err != nil {
		return nil, fmt.Errorf("routing: cannot load routes from %s: %w", dir, err)
	}

	var entries []*RouteEntry
	for _, route := range loader.All() {
		if !route.Extension.Exposed {
			return nil, fmt.Errorf("routing: extension %s %s must set exposed=true", route.Method, route.Path)
		}
		entry := &RouteEntry{
			Service:   "accounts", // all routes currently go to the api service
			Method:    string(route.Method),
			Path:      route.Path,
			Protected: route.Extension.Protected,
		}
		if err := applyGeneratedAuthorizationMetadata(entry); err != nil {
			return nil, fmt.Errorf("routing: %s %s: %w", entry.Method, entry.Path, err)
		}
		if entry.Procedure != "" {
			return nil, fmt.Errorf("routing: extension %s %s collides with generated descriptor procedure %s", entry.Method, entry.Path, entry.Procedure)
		}
		entries = append(entries, entry)
	}

	log.Printf("routing: loaded %d explicit REST extensions from %s", len(entries), dir)
	return entries, nil
}

// DefaultRoutingDir returns the default path to the routing/rest/ directory.
func DefaultRoutingDir() string {
	if env := os.Getenv("ROUTE_CONFIG_DIR"); env != "" {
		return env
	}
	// Try relative to the source file first (works in dev/test).
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Dir(filename)
		candidate := filepath.Join(dir, "..", "routing", "rest")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join("..", "routing", "rest")
}

// NewRouteMatcher builds a RouteMatcher from generated REST plus explicit
// extension entries and generated Connect entries keyed by exact path.
func NewRouteMatcher(restEntries, connectEntries []*RouteEntry) *RouteMatcher {
	m := &RouteMatcher{
		restRoutes:    make(map[string][]restRoute),
		connectRoutes: make(map[string]*RouteEntry),
	}

	for _, entry := range restEntries {
		method := strings.ToUpper(entry.Method)
		segments := strings.Split(entry.Path, "/")
		m.restRoutes[method] = append(m.restRoutes[method], restRoute{
			segments: segments,
			entry:    entry,
		})
	}

	for _, entry := range connectEntries {
		m.connectRoutes[entry.Path] = entry
	}

	return m
}

func (m *RouteMatcher) RequiredServices() []string {
	services := make(map[string]struct{})
	for _, routes := range m.restRoutes {
		for _, route := range routes {
			if route.entry.Service != "" && route.entry.Service != "self" {
				services[route.entry.Service] = struct{}{}
			}
		}
	}
	for _, entry := range m.connectRoutes {
		if entry.Service != "" && entry.Service != "self" {
			services[entry.Service] = struct{}{}
		}
	}
	out := make([]string, 0, len(services))
	for service := range services {
		out = append(out, service)
	}
	sort.Strings(out)
	return out
}

// MatchREST looks up a REST route by HTTP method and path.
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
	if isConnectPath(path) {
		if entry := m.MatchConnect(path); entry != nil {
			return entry
		}
	}
	return m.MatchREST(method, path)
}

func isConnectPath(path string) bool {
	if len(path) < 2 || path[0] != '/' {
		return false
	}
	rest := path[1:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return false
	}
	return strings.Contains(rest[:slashIdx], ".")
}

func matchSegments(pattern, request []string) bool {
	if len(pattern) != len(request) {
		return false
	}
	for i := range pattern {
		if isParam(pattern[i]) {
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

func isParam(segment string) bool {
	return len(segment) > 2 && segment[0] == '{' && segment[len(segment)-1] == '}'
}

// authMode returns "public" or "required" based on the entry's Protected field.
func authMode(entry *RouteEntry) string {
	if entry.Protected {
		return "required"
	}
	return "public"
}

// EnvoyRouteEntry is the format that envoy.go expects. This bridges
// the folder-based RouteEntry to the envoy config generator.
type EnvoyRouteEntry struct {
	Service string    `yaml:"service"`
	Rest    *RestPath `yaml:"rest"`
	Connect string    `yaml:"connect"`
	Rewrite string    `yaml:"rewrite,omitempty"`
	Auth    string    `yaml:"auth"`
}

type RestPath struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
}

// EnvoyRouteConfig wraps entries for the envoy generator.
type EnvoyRouteConfig struct {
	Routes []EnvoyRouteEntry
}

// ToEnvoyConfig converts folder-based RouteEntries to EnvoyRouteConfig.
func ToEnvoyConfig(entries []*RouteEntry) *EnvoyRouteConfig {
	cfg := &EnvoyRouteConfig{}
	for _, e := range entries {
		entry := EnvoyRouteEntry{Service: e.Service, Auth: authMode(e)}
		if strings.HasSuffix(e.Service, "_connect") {
			entry.Service = strings.TrimSuffix(e.Service, "_connect")
			entry.Connect = e.Path
			entry.Rewrite = e.UpstreamPath
		} else {
			entry.Rest = &RestPath{Method: e.Method, Path: e.Path}
		}
		cfg.Routes = append(cfg.Routes, entry)
	}
	return cfg
}
