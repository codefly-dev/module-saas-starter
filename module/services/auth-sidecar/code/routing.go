package main

// Route configuration loader for the auth-sidecar gateway.
//
// Loads route definitions from the folder-based *.rest.codefly.yaml files
// under routing/rest/{module}/{service}/. Each file is one endpoint group.
// Any request not matching an entry is rejected with 404 — zero prefix
// matching, zero implicit exposure.
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
	Service   string // upstream service name (from directory path)
	Method    string // HTTP method
	Path      string // HTTP path (may contain {param} segments)
	Protected bool   // true = auth required, false = public
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

// LoadRoutesFromDir loads all *.rest.codefly.yaml files from the routing
// directory using core's ExtendedRestRouteLoader. Returns RouteEntry slice.
func LoadRoutesFromDir(ctx context.Context, dir string) ([]*RouteEntry, error) {
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
			continue // not exposed — skip
		}
		entries = append(entries, &RouteEntry{
			Service: "accounts", // all routes currently go to the api service
			Method:    string(route.Method),
			Path:      route.Path,
			Protected: route.Extension.Protected,
		})
	}

	log.Printf("routing: loaded %d exposed routes from %s", len(entries), dir)
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

// NewRouteMatcher builds a RouteMatcher from REST and Connect entries.
// REST entries come from folder-based *.rest.codefly.yaml files; Connect
// entries come from LoadConnectRoutesFromProto and are keyed by exact path.
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
		cfg.Routes = append(cfg.Routes, EnvoyRouteEntry{
			Service: e.Service,
			Rest:    &RestPath{Method: e.Method, Path: e.Path},
			Auth:    authMode(e),
		})
	}
	return cfg
}
