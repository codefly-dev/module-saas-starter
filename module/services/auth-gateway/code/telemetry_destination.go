package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/codefly-dev/core/wool"
	wooltel "github.com/codefly-dev/core/wool/otel"
	codefly "github.com/codefly-dev/sdk-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Where this process sends traces and metrics — or that it sends none.
//
// The platform tells a workload through the `observability` configuration
// group, and the group always says one of two things:
//
//	TELEMETRY_STATE=available  with OTEL_EXPORTER_OTLP_ENDPOINT, the cell's
//	                           collector (OTLP/gRPC), e.g.
//	                           http://otel-collector.otel-collector.svc.cluster.local:4317
//	TELEMETRY_STATE=absent     with TELEMETRY_ABSENT_REASON, why the cell has no
//	                           collector
//
// `http://` is plaintext on the wire because the service mesh supplies mTLS
// between workloads; `https://` is TLS, which the metrics exporter dials but
// wool's tracer cannot yet (see errWoolCannotDialTLS). This module owns no
// collector: the cell does, and a workload that cannot find it does not
// substitute one.
//
// The state decides. The group is layered, so a key the state does not use can
// still arrive from a lower layer: it is ignored, not refused, and reported once
// through IgnoredNotice. What each state requires is unchanged: `available` needs
// an endpoint, `absent` needs a reason. The module's own profile ships neither
// the state nor the reason, so a cell that delivers `available` carries no
// leftover and a cell that delivers `absent` without a reason is refused.
//
// A group that did not arrive is not a cell without a backend. Reading the first
// as the second would leave a deployment that converges green and exports
// nothing, so a missing or unknown state, or the key the state requires being
// absent, refuses to start, naming what is missing. Only a local runtime may
// infer `absent` when the state is missing, and it supplies the reason itself
// (localAbsentReason): module defaults declare neither.
//
// This file exists byte for byte in services/accounts/code and
// services/auth-gateway/code: the two services are independent Go modules with
// no shared package, and module/tools/telemetry_destination_mirror_test.go holds
// the copies identical. Change one, copy it over the other.

const (
	telemetryStateKey        = "TELEMETRY_STATE"
	telemetryEndpointKey     = "OTEL_EXPORTER_OTLP_ENDPOINT"
	telemetryAbsentReasonKey = "TELEMETRY_ABSENT_REASON"

	telemetryStateAvailable = "available"
	telemetryStateAbsent    = "absent"

	// maxAbsentReasonLength bounds the operator-written reason before it reaches
	// a log line: an unbounded value from configuration would otherwise let
	// whoever writes the group forge log entries around it.
	maxAbsentReasonLength = 300

	// localAbsentReason is the reason a local run gives for having no collector.
	// It lives in code, and is used only when the process is local, because the
	// module's configuration profile is inherited by deployed cells: a reason
	// shipped there would reach a cell that sent `absent` without saying why.
	localAbsentReason = "A local run has no cell collector; traces go to the stdout tracer and metrics are not exported."
)

// telemetryDestination is the resolved answer. Exactly one of URL and
// AbsentReason is set.
type telemetryDestination struct {
	// URL is the collector's origin as scheme://host:port, the port always
	// explicit, which is the form otlpmetricgrpc.WithEndpointURL takes: the
	// exporter reads its transport off the scheme. Empty when the cell has no
	// collector.
	URL string
	// Endpoint is the same address as host:port. wool's tracer takes it in this
	// form and has no option that reads a URL. Empty when the cell has no
	// collector.
	Endpoint string
	// Insecure is true when the endpoint was http://, which the mesh protects.
	// wool's tracer needs it spelled out; it is derived from the endpoint's
	// scheme and never assumed.
	Insecure bool
	// AbsentReason says why the cell has no collector. Empty when URL is set.
	AbsentReason string
	// Ignored names the key the group carried that has no meaning for the state
	// that decided: the absent reason under `available`, the endpoint under
	// `absent`. Empty when the group carried nothing extra.
	Ignored string
}

// Available reports whether the cell has a collector to export to.
func (d telemetryDestination) Available() bool {
	return d.Endpoint != ""
}

// IgnoredNotice is the one line to log, once, when the group carried a key the
// state does not use. It is empty otherwise. It names the key and never its
// value: a leftover endpoint may carry credentials, and a leftover reason is
// free text.
func (d telemetryDestination) IgnoredNotice() string {
	if d.Ignored == "" {
		return ""
	}
	state := telemetryStateAbsent
	if d.Available() {
		state = telemetryStateAvailable
	}
	return fmt.Sprintf(
		"observability: %s is set but %s is `%s`, which decides; %s is ignored (a leftover from a lower layer of the group)",
		d.Ignored, telemetryStateKey, state, d.Ignored)
}

// observabilityValue reads one key of the `observability` group through the
// SDK. It deliberately has no fallback to a bare process variable: an ambient
// OTEL_EXPORTER_OTLP_ENDPOINT in a shell or a pod is conventionally the
// OTLP/HTTP address, not the cell's gRPC collector, and reading it here would
// export to the wrong place without a word.
func observabilityValue(key string) string {
	value, err := codefly.For(codefly.Context()).WorkspaceValue("observability", key)
	if err != nil {
		return ""
	}
	return value
}

// configuredTelemetryDestination resolves the destination from the
// `observability` group.
func configuredTelemetryDestination(local bool) (telemetryDestination, error) {
	return resolveTelemetryDestination(local, observabilityValue)
}

// resolveTelemetryDestination is the whole decision, over any reader of the
// group's keys so a test can reach every outcome with nothing running.
func resolveTelemetryDestination(local bool, read func(key string) string) (telemetryDestination, error) {
	state := strings.TrimSpace(read(telemetryStateKey))
	endpoint := strings.TrimSpace(read(telemetryEndpointKey))
	reason := boundedLine(read(telemetryAbsentReasonKey), maxAbsentReasonLength)

	if state == "" && local {
		state = telemetryStateAbsent
		if reason == "" {
			reason = localAbsentReason
		}
	}
	if state == "" {
		return telemetryDestination{}, errors.New(
			"observability: " + telemetryStateKey + " is not set in the `observability` configuration group. " +
				"The platform delivers `" + telemetryStateAvailable + "` (with " + telemetryEndpointKey + ") or `" +
				telemetryStateAbsent + "` (with " + telemetryAbsentReasonKey + "); a group that did not arrive is " +
				"not a cell without a collector, so this process does not start without it")
	}
	if state != telemetryStateAvailable && state != telemetryStateAbsent {
		return telemetryDestination{}, fmt.Errorf(
			"observability: %s is %q in the `observability` configuration group; it must be `%s` or `%s`",
			telemetryStateKey, state, telemetryStateAvailable, telemetryStateAbsent)
	}

	// The state decides: the key that belongs to the other state is a leftover
	// of a lower layer and is neither read nor refused. The endpoint under
	// `absent` is not even parsed, so a stale one cannot stop a cell that says
	// it has no collector.
	if state == telemetryStateAbsent {
		if reason == "" {
			return telemetryDestination{}, fmt.Errorf(
				"observability: %s is `%s` but %s is not set in the `observability` configuration group; "+
					"an absent collector must say why",
				telemetryStateKey, telemetryStateAbsent, telemetryAbsentReasonKey)
		}
		destination := telemetryDestination{AbsentReason: reason}
		if endpoint != "" {
			destination.Ignored = telemetryEndpointKey
		}
		return destination, nil
	}

	if endpoint == "" {
		return telemetryDestination{}, fmt.Errorf(
			"observability: %s is `%s` but %s is not set in the `observability` configuration group",
			telemetryStateKey, telemetryStateAvailable, telemetryEndpointKey)
	}
	origin, hostPort, insecure, err := parseCollectorEndpoint(endpoint)
	if err != nil {
		return telemetryDestination{}, fmt.Errorf("observability: %s: %w", telemetryEndpointKey, err)
	}
	destination := telemetryDestination{URL: origin, Endpoint: hostPort, Insecure: insecure}
	if reason != "" {
		destination.Ignored = telemetryAbsentReasonKey
	}
	return destination, nil
}

// parseCollectorEndpoint turns the delivered URL into the two forms the
// exporters take — the origin with an explicit port, and host:port — and reads
// the transport off its scheme. The URL is never echoed back: a malformed one
// may carry credentials.
func parseCollectorEndpoint(raw string) (origin, hostPort string, insecure bool, err error) {
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", "", false, errors.New("is not a URL; expected http://host:port or https://host:port")
	}
	switch parsed.Scheme {
	case "http":
		insecure = true
	case "https":
	default:
		return "", "", false, fmt.Errorf("must start with http:// or https://, got scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return "", "", false, errors.New("has no host")
	}
	if parsed.User != nil {
		return "", "", false, errors.New("must not carry credentials")
	}
	// OTLP/gRPC addresses a collector, not a path: a path or a query is the
	// OTLP/HTTP form (/v1/traces), which this exporter does not speak.
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", false, errors.New("must be the collector's origin only, with no path, query or fragment")
	}
	// The port is made explicit because gRPC would otherwise dial 443 whatever
	// the scheme, and a plaintext http:// collector with no port is not on 443.
	port := parsed.Port()
	if port == "" {
		port = "80"
		if !insecure {
			port = "443"
		}
	}
	hostPort = net.JoinHostPort(parsed.Hostname(), port)
	return parsed.Scheme + "://" + hostPort, hostPort, insecure, nil
}

// boundedLine collapses a free-text value to one bounded line.
func boundedLine(value string, limit int) string {
	line := strings.Join(strings.Fields(value), " ")
	if runes := []rune(line); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return line
}

// telemetryResource is the one resource both signals carry. service.name is
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

// telemetryServiceName is the service.name the resource resolved to.
func telemetryServiceName(res *resource.Resource) string {
	if value, ok := res.Set().Value(attribute.Key("service.name")); ok {
		return value.AsString()
	}
	return ""
}

// errWoolCannotDialTLS names the one transport the tracer cannot serve.
var errWoolCannotDialTLS = errors.New(
	"the collector endpoint is https://, but wool's OTLP tracer cannot dial TLS: wool/otel.Enable " +
		"always sets an insecure transport and offers no option to turn it off. Deliver an http:// " +
		"endpoint (in-mesh, where the mesh supplies mTLS) until wool can dial TLS")

// enableTracing registers wool's tracer for the destination: OTLP/gRPC to the
// cell's collector when there is one; wool's own stdout tracer when there is
// none and this is a local run; nothing otherwise. A nil provider means nothing
// was registered.
func enableTracing(ctx context.Context, destination telemetryDestination, local bool) (*wooltel.Provider, error) {
	res, err := currentTelemetryResource(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve telemetry resource: %w", err)
	}
	options := []wooltel.Option{wooltel.WithServiceName(telemetryServiceName(res))}
	switch {
	case destination.Available():
		// wool dials plaintext unless told otherwise and cannot be told
		// otherwise, so an https:// endpoint is refused rather than dialed in
		// the clear and reported as TLS.
		if !destination.Insecure {
			return nil, errWoolCannotDialTLS
		}
		options = append(options, wooltel.WithEndpoint(destination.Endpoint), wooltel.WithInsecure())
	case local:
		options = append(options, wooltel.WithStdout())
	default:
		return nil, nil
	}
	return wooltel.Enable(options...)
}
