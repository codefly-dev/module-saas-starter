package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/codefly-dev/module-saas-starter/module/frontendpayload"
)

type preparedFrontend struct {
	frontend *solutionhost.Frontend
	ready    bool
}

// Prepare only current declarations. Accounts resolves historical versions on
// demand, so retained history cannot inflate the shared route snapshot.
func (c *solutionRegistryCache) prepareFrontends(ctx context.Context, snapshot *solutionRegistrySnapshot) map[string]map[string]*preparedFrontend {
	out := map[string]map[string]*preparedFrontend{}
	for _, record := range snapshot.GetRegistrations() {
		if record.GetTombstonedAt() != nil || record.GetDeclared().GetTargetId() == "" {
			continue
		}
		var frontend solutionhost.Frontend
		if json.Unmarshal([]byte(record.GetFrontend().GetManifest()), &frontend) != nil || frontend.Payload == nil {
			continue
		}
		bundle, _ := c.prepareBundle(ctx, &frontend)
		out[record.GetSolutionId()] = map[string]*preparedFrontend{frontendAssetVersion(record): {frontend: &frontend, ready: bundle != nil}}
	}
	return out
}

func (c *solutionRegistryCache) retainedFrontend(ctx context.Context, routing *solutionRouting, version string) (*preparedFrontend, error) {
	reader, ok := c.client.(solutionFrontendReader)
	if !ok {
		return nil, errSolutionRegistryUnconfigured
	}
	ctx, cancel := context.WithTimeout(ctx, solutionRegistryCallTimeout)
	defer cancel()
	found, err := reader.GetRetainedFrontend(ctx, routing.BindingID, version)
	if err != nil || found == nil {
		return nil, err
	}
	if found.GetBindingId() != routing.BindingID || found.GetTargetId() != routing.TargetID || found.GetAssetVersion() != version ||
		frontendpayload.AssetVersion(found.GetBindingId(), found.GetTargetId(), found.GetGeneration(), found.GetRelease()) != version {
		return nil, fmt.Errorf("retained frontend identity mismatch")
	}
	var frontend solutionhost.Frontend
	if err := json.Unmarshal([]byte(found.GetFrontend()), &frontend); err != nil || frontend.Payload == nil {
		return nil, fmt.Errorf("retained frontend has no signed payload")
	}
	return &preparedFrontend{frontend: &frontend}, nil
}

// One acquisition at a time coalesces concurrent misses and bounds scratch
// allocation. Four bundles bound all cached bytes to 256 MiB; route snapshots
// retain only preparation metadata. Eight active asset handlers bound held reads.
func (c *solutionRegistryCache) prepareBundle(ctx context.Context, frontend *solutionhost.Frontend) (*frontendpayload.Bundle, error) {
	ctx, cancel := context.WithTimeout(ctx, solutionRegistryCallTimeout)
	defer cancel()
	select {
	case c.preparing <- struct{}{}:
		defer func() { <-c.preparing }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	encoded, err := json.Marshal(frontend)
	if err != nil {
		return nil, err
	}
	key := string(encoded)
	if bundle := c.bundles[key]; bundle != nil {
		return bundle, nil
	}
	bundle, err := frontendpayload.Prepare(ctx, c.frontendSource, frontend)
	if err != nil {
		return nil, err
	}
	if len(c.bundleOrder) == 4 {
		delete(c.bundles, c.bundleOrder[0])
		c.bundleOrder = c.bundleOrder[1:]
	}
	c.bundles[key] = bundle
	c.bundleOrder = append(c.bundleOrder, key)
	return bundle, nil
}

func (c *solutionRegistryCache) frontendPrepared(record *accountsv1.SolutionRegistration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	prepared := c.frontends[record.GetSolutionId()][frontendAssetVersion(record)]
	return prepared != nil && prepared.ready
}

func (g *Gateway) serveFrontendBundle(w http.ResponseWriter, r *http.Request, routing *solutionRouting, publicPath string) {
	version := r.Header.Get(frontendVersionHeader)
	if version == "" {
		version = routing.AssetVersion
	}
	if !frontendVersionPattern.MatchString(version) {
		w.Header().Set("Cache-Control", "no-store")
		httpError(w, http.StatusGone, "frontend generation is no longer served")
		return
	}
	entry := &RouteEntry{Service: "frontend:" + routing.BindingID, RateLimitClass: edgeRateLimitClassPublic}
	g.rateLimitThenServe(w, r, entry, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case g.solutions.serving <- struct{}{}:
			defer func() { <-g.solutions.serving }()
		default:
			httpError(w, http.StatusServiceUnavailable, "frontend serving capacity is busy")
			return
		}
		selected := routing.Frontends[version]
		if selected == nil {
			var err error
			selected, err = g.solutions.retainedFrontend(r.Context(), routing, version)
			if err != nil {
				w.Header().Set("Cache-Control", "no-store")
				httpError(w, http.StatusServiceUnavailable, "frontend history is unavailable")
				return
			}
		}
		if selected == nil {
			w.Header().Set("Cache-Control", "no-store")
			httpError(w, http.StatusGone, "frontend generation is no longer served")
			return
		}
		bundle, err := g.solutions.prepareBundle(r.Context(), selected.frontend)
		if err != nil {
			w.Header().Set("Cache-Control", "no-store")
			httpError(w, http.StatusServiceUnavailable, "frontend payload is unavailable")
			return
		}
		// Preparation can race a refresh or outlast authority. Bytes in memory
		// are never evidence that their target is still open.
		latest := g.solutions.lookupSnapshot(routing.Alias)
		if !latest.loaded || latest.stale {
			w.Header().Set("Cache-Control", "no-store")
			httpError(w, http.StatusServiceUnavailable, "solution registry snapshot is stale")
			return
		}
		if latest.record.GetTombstonedAt() != nil || latest.record.GetDeclared().GetTargetId() != routing.TargetID || latest.record.GetDeclared().GetBindingId() != routing.BindingID {
			w.Header().Set("Cache-Control", "no-store")
			httpError(w, http.StatusGone, "frontend generation is no longer served")
			return
		}
		name := strings.TrimPrefix(publicPath, "/assets/")
		reader, _, err := bundle.Open(name)
		if err != nil {
			w.Header().Set("Cache-Control", "no-store")
			httpError(w, http.StatusNotFound, "frontend asset not found")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get(frontendVersionHeader) != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, time.Time{}, reader)
	}))
}
