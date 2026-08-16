// Package moduleinfo aggregates per-service introspection responses
// (IntrospectionService.GetServiceInfo) into a module-level view.
//
// In codefly's architecture: a module is a collection of services;
// each service owns its own proto and exposes its own introspection.
// To answer "what can the saas-starter module do?", a consumer
// (CLI / gateway / Mind) walks every service in module.codefly.yaml
// and merges each service's GetServiceInfo response.
//
// This package is the glue: takes a list of service-info URLs,
// calls each, and produces a merged view. The proto definitions
// stay where they are (in each service's own proto/) — this
// aggregator just consumes the JSON shape.
//
// Today saas-starter has only the accounts service exposing
// IntrospectionService. As other services (auth-sidecar, etc.) gain
// it, add their endpoints to the input list.
package moduleinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// ServiceInfo mirrors the accounts service's gen.ServiceInfo (we
// re-declare it here so this package has zero dependency on
// api/pkg/gen — anything that calls a codefly service's
// /v1/.well-known/service-info gets a JSON of the same shape).
type ServiceInfo struct {
	Name        string `json:"name"`
	Module      string `json:"module"`
	Version     string `json:"version"`
	Description string `json:"description"`
	RepoURL     string `json:"repoUrl,omitempty"`
}

type RPCInfo struct {
	Service      string   `json:"service"`
	Method       string   `json:"method"`
	HTTPMethod   string   `json:"httpMethod,omitempty"`
	HTTPPath     string   `json:"httpPath,omitempty"`
	Description  string   `json:"description,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	HandlerAuthz string   `json:"handlerAuthz,omitempty"`
	EmitsAudit   bool     `json:"emitsAudit,omitempty"`
}

type PermissionInfo struct {
	Resource     string   `json:"resource"`
	Action       string   `json:"action"`
	Description  string   `json:"description,omitempty"`
	BuiltInRoles []string `json:"builtInRoles,omitempty"`
}

type RLSPolicyInfo struct {
	Table       string `json:"table"`
	PolicyShape string `json:"policyShape"`
	FailClosed  bool   `json:"failClosed"`
	ScopeColumn string `json:"scopeColumn,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

type ScopeInfo struct {
	Scope       string `json:"scope"`
	Description string `json:"description,omitempty"`
}

// ServiceCapabilities is one service's GetServiceInfo response body.
type ServiceCapabilities struct {
	Info        ServiceInfo      `json:"info"`
	RPCs        []RPCInfo        `json:"rpcs,omitempty"`
	Permissions []PermissionInfo `json:"permissions,omitempty"`
	RLSTables   []RLSPolicyInfo  `json:"rlsTables,omitempty"`
	Scopes      []ScopeInfo      `json:"scopes,omitempty"`
}

type getServiceInfoResponse struct {
	Capabilities *ServiceCapabilities `json:"capabilities"`
}

// ModuleView is the merged view across every service in the module.
// `Services` is the per-service slice (one entry per upstream).
// `Aggregate.RPCs` is every RPC across all services concatenated;
// likewise for the other lists.
type ModuleView struct {
	// ModuleName comes from the first service's ServiceInfo.Module
	// (we assume all services agree; we warn if they don't).
	ModuleName string `json:"moduleName"`
	// Services is one entry per upstream queried; preserves
	// per-service identity so consumers can drill in.
	Services []ServiceCapabilities `json:"services"`
	// Aggregate is the cross-service flattened view: every RPC,
	// permission, RLS table, scope across the module. Useful for
	// "show me everything" tools (Mind, security audits).
	Aggregate ServiceCapabilities `json:"aggregate"`
}

// Endpoint identifies one service's introspection URL. Name is
// informational (used in error messages); URL is where to GET.
type Endpoint struct {
	Name string
	URL  string // e.g. "http://localhost:5962/v1/.well-known/service-info"
}

// Aggregate calls each endpoint's introspection RPC in parallel and
// merges. Failures on individual services are reported in the
// returned error map but don't abort the whole call (a half-merged
// view is more useful than nothing).
//
// timeout caps the total wait per endpoint; 5s is reasonable.
func Aggregate(ctx context.Context, endpoints []Endpoint, timeout time.Duration) (*ModuleView, map[string]error) {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	type result struct {
		ep   Endpoint
		caps *ServiceCapabilities
		err  error
	}
	out := make(chan result, len(endpoints))
	for _, ep := range endpoints {
		ep := ep
		go func() {
			caps, err := fetchOne(ctx, ep.URL, timeout)
			out <- result{ep: ep, caps: caps, err: err}
		}()
	}

	view := &ModuleView{}
	errs := map[string]error{}
	for i := 0; i < len(endpoints); i++ {
		r := <-out
		if r.err != nil {
			errs[r.ep.Name] = r.err
			continue
		}
		view.Services = append(view.Services, *r.caps)
	}
	close(out)

	// Stable sort BEFORE the mismatch check so "first" is
	// deterministic regardless of goroutine ordering. Otherwise
	// which service wins the module-name slot would depend on the
	// race.
	sort.Slice(view.Services, func(i, j int) bool {
		return view.Services[i].Info.Name < view.Services[j].Info.Name
	})

	// Module name: take the first (alphabetically) service's. Flag
	// any service that reports a different module — useful when
	// someone wires the wrong endpoint into module-info.
	for i, sc := range view.Services {
		if i == 0 {
			view.ModuleName = sc.Info.Module
			continue
		}
		if sc.Info.Module != view.ModuleName && view.ModuleName != "" {
			errs[sc.Info.Name] = fmt.Errorf(
				"service %q reports module=%q but aggregator already saw module=%q — services from different modules?",
				sc.Info.Name, sc.Info.Module, view.ModuleName,
			)
		}
	}
	for _, sc := range view.Services {
		view.Aggregate.RPCs = append(view.Aggregate.RPCs, sc.RPCs...)
		view.Aggregate.Permissions = append(view.Aggregate.Permissions, sc.Permissions...)
		view.Aggregate.RLSTables = append(view.Aggregate.RLSTables, sc.RLSTables...)
		view.Aggregate.Scopes = append(view.Aggregate.Scopes, sc.Scopes...)
	}
	return view, errs
}

func fetchOne(ctx context.Context, url string, timeout time.Duration) (*ServiceCapabilities, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	var out getServiceInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if out.Capabilities == nil {
		return nil, fmt.Errorf("response missing 'capabilities' field")
	}
	return out.Capabilities, nil
}
