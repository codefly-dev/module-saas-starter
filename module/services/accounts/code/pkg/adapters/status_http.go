package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"accounts/pkg/business"
)

// StatusProbe runs one health check and returns its result. Wired by
// work.go for postgres / redis / vault — anything else can plug in.
// Probes have a 2s budget; longer counts as degraded.
type StatusProbe struct {
	Name  string
	Check func(ctx context.Context) error
}

// StatusComponent is the JSON-shape one row in the status response.
type StatusComponent struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // "ok" | "degraded" | "down"
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// StatusResponse is what /v1/status returns.
type StatusResponse struct {
	Status     string            `json:"status"` // worst component status
	CheckedAt  time.Time         `json:"checked_at"`
	Components []StatusComponent `json:"components"`
	// Uptime — process uptime in seconds. Useful for admins eyeballing
	// "did the api just restart?" without reading logs.
	UptimeSeconds int64 `json:"uptime_seconds"`
}

var (
	statusProbes       []StatusProbe
	statusProbesMu     sync.RWMutex
	statusProcessStart = time.Now()
)

// RegisterStatusProbe adds a health check to the /v1/status endpoint.
// Idempotent on (name) — a second registration replaces the first.
// Called from work.go after the relevant clients are wired.
func RegisterStatusProbe(p StatusProbe) {
	statusProbesMu.Lock()
	defer statusProbesMu.Unlock()
	for i, existing := range statusProbes {
		if existing.Name == p.Name {
			statusProbes[i] = p
			return
		}
	}
	statusProbes = append(statusProbes, p)
}

// NewStatusHTTPHandler returns the /v1/status handler. Public —
// intentionally not gated by auth. The endpoint is part of the SLA
// surface (status pages, health checks, customer-side monitoring),
// not internal admin diagnostics.
func NewStatusHTTPHandler(svc *business.Service) http.Handler {
	_ = svc // reserved for future probes that need DB lookups beyond the registered StatusProbes
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		statusProbesMu.RLock()
		probes := append([]StatusProbe(nil), statusProbes...)
		statusProbesMu.RUnlock()

		comps := make([]StatusComponent, len(probes))
		// Run probes in parallel — a slow component shouldn't block
		// the rest. 2s per-probe budget enforced by the contexts they
		// build from `ctx`.
		var wg sync.WaitGroup
		for i, p := range probes {
			wg.Add(1)
			go func(i int, p StatusProbe) {
				defer wg.Done()
				probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				start := time.Now()
				err := p.Check(probeCtx)
				latency := time.Since(start)
				comps[i] = StatusComponent{
					Name:      p.Name,
					LatencyMs: latency.Milliseconds(),
				}
				switch {
				case err == nil:
					comps[i].Status = "ok"
				case latency >= 2*time.Second:
					comps[i].Status = "down"
					comps[i].Error = err.Error()
				default:
					comps[i].Status = "degraded"
					comps[i].Error = err.Error()
				}
			}(i, p)
		}
		wg.Wait()

		// Overall = worst component. "ok" is the default when no
		// probes registered (so a totally bare api still returns 200).
		overall := "ok"
		for _, c := range comps {
			if c.Status == "down" {
				overall = "down"
				break
			}
			if c.Status == "degraded" {
				overall = "degraded"
			}
		}

		resp := StatusResponse{
			Status:        overall,
			CheckedAt:     time.Now().UTC(),
			Components:    comps,
			UptimeSeconds: int64(time.Since(statusProcessStart).Seconds()),
		}

		w.Header().Set("Content-Type", "application/json")
		// Status page tooling sometimes expects 503 when degraded.
		// 200 / 503 split makes /status play nicely with k8s liveness
		// probes too — anything non-200 = unhealthy.
		switch overall {
		case "down":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "degraded":
			w.WriteHeader(http.StatusOK) // partial degradation is still serving
		default:
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}
