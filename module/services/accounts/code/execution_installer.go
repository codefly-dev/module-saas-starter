package main

import (
	"accounts/pkg/adapters"
	"accounts/pkg/business"
	"net/http"
	"os"
)

func configuredModuleInstaller(service *business.Service) (http.Handler, error) {
	path := os.Getenv("MODULE_INSTALLER_POLICY_FILE")
	if path == "" {
		return nil, nil
	}
	return adapters.NewModuleInstallationHTTPHandler(service, path)
}

// The private listener and REST endpoint share one policy-backed handler. Only
// the four installer operations are added; custody and JWKS keep their existing
// dispatch, TLS identity and authentication. No public REST mux is mounted here.
func mountExecutionInstaller(host *executionCustodyHost, installer http.Handler) {
	if host == nil || installer == nil {
		return
	}
	fallback := host.broker.Handler
	host.broker.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/module-installations/token", "/v1/module-installations/inspect",
			"/v1/module-installations/apply", "/v1/module-installations/verify":
			installer.ServeHTTP(w, r)
		default:
			fallback.ServeHTTP(w, r)
		}
	})
}
