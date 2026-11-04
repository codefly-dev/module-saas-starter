package adapters

import (
	"net/url"
	"os"
	"strings"

	"github.com/rs/cors"
)

/* -----------------------------------------------------------------

STOPGAP (codefly-dev/service-go-grpc#223): this file is hand-maintained.
It is not the go-grpc agent's output, although its name and layout follow the
agent's template. The agent renders these adapters only for a service whose
proto declares a single AccountsService. Accounts serves many proto services
and declares no AccountsService, so the agent's Sync never writes this file
(go-grpc 0.1.72, generatedScaffoldTargets). It stays hand-maintained until #223
lets the agent render this service's shape; adopt the generated file then,
keeping the CUSTOM lines. main.go and plugins/registry.go carry the same
stopgap.

----------------------------------------------------------------- */

func Cors() *cors.Cors {
	return cors.New(cors.Options{
		AllowedOrigins: configuredCORSOrigins(),
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowedHeaders: []string{
			"Authorization",
			"Content-Type",
			"Accept",
			"Origin",
			"X-Requested-With",
			"X-Request-ID",
			"Idempotency-Key",
		},
		AllowCredentials: true,
		// Handle preflight: rs/cors automatically responds to OPTIONS
		// with the correct Access-Control-Allow-* headers when
		// OptionsPassthrough is false (the default).
	})
}

func configuredCORSOrigins() []string {
	var origins []string
	for _, o := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		o = strings.TrimSpace(o)
		if validExactOrigin(o) {
			origins = append(origins, o)
		}
	}
	return origins
}

func validExactOrigin(origin string) bool {
	if origin == "" || strings.ContainsAny(origin, "*\r\n") {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
