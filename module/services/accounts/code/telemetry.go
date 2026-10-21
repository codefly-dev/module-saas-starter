package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// otlpEndpointVariable is the whole telemetry contract: OpenTelemetry's own name
// for the collector's address. The platform delivers it when the cell has a
// collector this workload may reach, and delivers nothing when it does not.
//
// Deliver it and the process exports traces and metrics over OTLP/gRPC; deliver
// none and it exports nothing over OTLP. Nothing here reads a state, a reason or
// a protocol: core's wool/otel reads this variable itself, tells a URL from a
// host:port, takes the transport from the scheme, validates the URL and refuses a
// portless one, and the metrics exporter reads it from the same environment.
const otlpEndpointVariable = "OTEL_EXPORTER_OTLP_ENDPOINT"

// otlpEndpointConfigured reports whether the process was given a collector. A
// whitespace-only value is none: the OpenTelemetry metrics exporter trims what it
// reads and treats an empty result as unset, and an exporter with nothing
// configured dials its own default, localhost:4317.
func otlpEndpointConfigured() bool {
	return strings.TrimSpace(os.Getenv(otlpEndpointVariable)) != ""
}

// requireMetricsEndpoint refuses what the metrics exporter cannot read. It reads
// the variable only as an http:// or https:// URL, and on anything else keeps its
// default and dials localhost:4317 without saying so, so a value of another shape
// is rejected here instead of creating a metrics exporter at a default address.
func requireMetricsEndpoint() error {
	if !otlpEndpointConfigured() {
		return errors.New("no " + otlpEndpointVariable + " to export metrics to")
	}
	parsed, err := url.Parse(strings.TrimSpace(os.Getenv(otlpEndpointVariable)))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New(otlpEndpointVariable + " must be an http:// or https:// URL for metrics to be exported; the exporter ignores any other form and would fall back to its own default address")
	}
	return nil
}

// telemetryResource builds the metrics resource. service.name is
// whatever the environment says — OTEL_SERVICE_NAME, or service.name in
// OTEL_RESOURCE_ATTRIBUTES — and otherwise the service identity the Codefly SDK
// holds. No name is typed here: a literal would override the environment, so the
// platform's identity could never reach the backend.
func telemetryResource(ctx context.Context, identity string) (*resource.Resource, error) {
	options := []resource.Option{resource.WithService()}
	if identity != "" {
		options = append(options, resource.WithAttributes(attribute.String("service.name", identity)))
	}
	// Last, so what the environment declares wins over the identity above.
	options = append(options, resource.WithFromEnv())
	return resource.New(ctx, options...)
}

// currentTelemetryResource is telemetryResource for this process.
func currentTelemetryResource(ctx context.Context) (*resource.Resource, error) {
	return telemetryResource(ctx, codeflyServiceName(codefly.Context()))
}

// codeflyServiceName is the service identity the Codefly SDK holds: the
// `module/service` identity of the provider it built at Init, which wool stamps on
// every log line from this process. It is empty when the SDK has not been
// initialised. The SDK exposes no accessor of its own, and the environment
// carrier it reads this from is not product code's to read, so this asks the
// provider.
func codeflyServiceName(ctx context.Context) string {
	provider, ok := ctx.Value(wool.ProviderKey).(*wool.Provider)
	if !ok || provider == nil {
		return ""
	}
	source := provider.Get(ctx).Source()
	if source == nil {
		return ""
	}
	// An identity with no module reads `/service`; with neither, `/`.
	return strings.Trim(strings.TrimSpace(source.Unique), "/")
}
