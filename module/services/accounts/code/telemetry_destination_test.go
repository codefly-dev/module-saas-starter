package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	collectortracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// group is a reader over a fixed set of `observability` keys.
func group(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

const collector = "http://otel-collector.otel-collector.svc.cluster.local:4317"

// Module defaults are also inherited by cells. They must not supply the state
// that only the platform can declare for a deployed workload.
func TestTelemetryDestinationShippedDefaultsCannotMaskMissingCellState(t *testing.T) {
	contents, err := os.ReadFile("../../../configurations/local/observability.env")
	require.NoError(t, err)
	values := map[string]string{}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok)
		values[key] = value
	}
	_, err = resolveTelemetryDestination(false, group(values))
	require.ErrorContains(t, err, "TELEMETRY_STATE is not set")
	destination, err := resolveTelemetryDestination(true, group(values))
	require.NoError(t, err)
	require.False(t, destination.Available())
	require.NotEmpty(t, destination.AbsentReason)
}

func TestTelemetryDestinationMissingStateDefaultsOnlyInLocalRuntime(t *testing.T) {
	for _, values := range []map[string]string{
		{},
		{"TELEMETRY_STATE": "  "},
		{"TELEMETRY_ABSENT_REASON": "local defaults inherited by a cell"},
		{"OTEL_EXPORTER_OTLP_ENDPOINT": collector},
	} {
		_, err := resolveTelemetryDestination(false, group(values))
		require.ErrorContains(t, err, "TELEMETRY_STATE is not set")
		destination, err := resolveTelemetryDestination(true, group(values))
		require.NoError(t, err)
		require.False(t, destination.Available())
		require.NotEmpty(t, destination.AbsentReason)
	}
	for _, values := range []map[string]string{
		{"TELEMETRY_STATE": "unknown"},
		{"TELEMETRY_STATE": "available"},
		{"TELEMETRY_STATE": "absent"},
	} {
		_, err := resolveTelemetryDestination(true, group(values))
		require.Error(t, err, "an explicit state must be valid and complete even locally")
	}
}

// The first outcome: the platform delivered a collector.
func TestTelemetryDestinationAvailableExportsToTheEndpoint(t *testing.T) {
	destination, err := resolveTelemetryDestination(false, group(map[string]string{
		"TELEMETRY_STATE":             "available",
		"OTEL_EXPORTER_OTLP_ENDPOINT": collector,
	}))
	require.NoError(t, err)
	require.True(t, destination.Available())
	require.Equal(t, collector, destination.URL, "the metrics exporter reads its transport off this URL")
	require.Equal(t, "otel-collector.otel-collector.svc.cluster.local:4317", destination.Endpoint)
	require.True(t, destination.Insecure, "http:// is plaintext on the wire; the mesh supplies mTLS")
	require.Empty(t, destination.AbsentReason)
	require.Empty(t, destination.Ignored)
	require.Empty(t, destination.IgnoredNotice())
}

// The transport comes from the endpoint's scheme, never from a constant, and
// the URL handed to the metrics exporter always carries an explicit port: gRPC
// would otherwise dial 443 whatever the scheme.
func TestTelemetryDestinationReadsTransportFromTheScheme(t *testing.T) {
	for endpoint, want := range map[string]struct {
		url      string
		hostPort string
		insecure bool
	}{
		"http://collector.example:4317":      {"http://collector.example:4317", "collector.example:4317", true},
		"https://collector.example:4317":     {"https://collector.example:4317", "collector.example:4317", false},
		"https://collector.example":          {"https://collector.example:443", "collector.example:443", false},
		"http://collector.example":           {"http://collector.example:80", "collector.example:80", true},
		"http://collector.example:4317/":     {"http://collector.example:4317", "collector.example:4317", true},
		"  http://collector.example:4317  ":  {"http://collector.example:4317", "collector.example:4317", true},
		"http://[2001:db8::1]:4317":          {"http://[2001:db8::1]:4317", "[2001:db8::1]:4317", true},
		"https://10.0.0.7:4317":              {"https://10.0.0.7:4317", "10.0.0.7:4317", false},
		"http://otel-collector.ns.svc:4317/": {"http://otel-collector.ns.svc:4317", "otel-collector.ns.svc:4317", true},
	} {
		destination, err := resolveTelemetryDestination(false, group(map[string]string{
			"TELEMETRY_STATE":             "available",
			"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		}))
		require.NoError(t, err, endpoint)
		require.Equal(t, want.url, destination.URL, endpoint)
		require.Equal(t, want.hostPort, destination.Endpoint, endpoint)
		require.Equal(t, want.insecure, destination.Insecure, endpoint)
	}
}

// The second outcome: the cell has no collector, and says why. The process boots
// and exports nothing.
func TestTelemetryDestinationAbsentCarriesTheReason(t *testing.T) {
	destination, err := resolveTelemetryDestination(false, group(map[string]string{
		"TELEMETRY_STATE":         "absent",
		"TELEMETRY_ABSENT_REASON": "This cell's bucket has no ops cell, so no collector is rendered.",
	}))
	require.NoError(t, err)
	require.False(t, destination.Available())
	require.Empty(t, destination.URL)
	require.Empty(t, destination.Endpoint)
	require.Equal(t, "This cell's bucket has no ops cell, so no collector is rendered.", destination.AbsentReason)
	require.Empty(t, destination.Ignored)
	require.Empty(t, destination.IgnoredNotice())
}

// The reason is operator-written free text that reaches a log line.
func TestTelemetryDestinationAbsentReasonIsOneBoundedLine(t *testing.T) {
	destination, err := resolveTelemetryDestination(false, group(map[string]string{
		"TELEMETRY_STATE":         "absent",
		"TELEMETRY_ABSENT_REASON": "no collector\n2026-01-01 FORGED entry\r\n" + strings.Repeat("x", 1000),
	}))
	require.NoError(t, err)
	require.NotContains(t, destination.AbsentReason, "\n")
	require.NotContains(t, destination.AbsentReason, "\r")
	require.LessOrEqual(t, len([]rune(destination.AbsentReason)), maxAbsentReasonLength+1)
}

// The third outcome: anything else refuses to start, naming what is wrong.
// Configuration that did not arrive is never read as "no collector". A key the
// state does not use is not a refusal — it is ignored, below — but a key the
// state requires, missing, always is.
func TestTelemetryDestinationRefusesEverythingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		values map[string]string
		want   string
	}{
		"state missing": {
			values: map[string]string{},
			want:   "TELEMETRY_STATE is not set",
		},
		"state missing though an ambient endpoint is present": {
			values: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": collector},
			want:   "TELEMETRY_STATE is not set",
		},
		"state missing though a reason is present": {
			values: map[string]string{"TELEMETRY_ABSENT_REASON": "no collector"},
			want:   "TELEMETRY_STATE is not set",
		},
		"state blank": {
			values: map[string]string{"TELEMETRY_STATE": "   "},
			want:   "TELEMETRY_STATE is not set",
		},
		"state unknown": {
			values: map[string]string{"TELEMETRY_STATE": "disabled", "TELEMETRY_ABSENT_REASON": "no collector"},
			want:   `TELEMETRY_STATE is "disabled"`,
		},
		"state is not matched case-insensitively": {
			values: map[string]string{"TELEMETRY_STATE": "Available", "OTEL_EXPORTER_OTLP_ENDPOINT": collector},
			want:   `TELEMETRY_STATE is "Available"`,
		},
		"available without an endpoint": {
			values: map[string]string{"TELEMETRY_STATE": "available"},
			want:   "OTEL_EXPORTER_OTLP_ENDPOINT is not set",
		},
		"available with only a reason": {
			values: map[string]string{"TELEMETRY_STATE": "available", "TELEMETRY_ABSENT_REASON": "no collector"},
			want:   "OTEL_EXPORTER_OTLP_ENDPOINT is not set",
		},
		"absent without a reason": {
			values: map[string]string{"TELEMETRY_STATE": "absent"},
			want:   "TELEMETRY_ABSENT_REASON is not set",
		},
		"absent with a blank reason": {
			values: map[string]string{"TELEMETRY_STATE": "absent", "TELEMETRY_ABSENT_REASON": " \n "},
			want:   "TELEMETRY_ABSENT_REASON is not set",
		},
		"absent with only an endpoint": {
			values: map[string]string{"TELEMETRY_STATE": "absent", "OTEL_EXPORTER_OTLP_ENDPOINT": collector},
			want:   "TELEMETRY_ABSENT_REASON is not set",
		},
		"endpoint without a scheme": {
			values: map[string]string{"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": "otel-collector.ns.svc:4317"},
			want:   "must start with http:// or https://",
		},
		"endpoint with another scheme": {
			values: map[string]string{"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": "grpc://otel-collector.ns.svc:4317"},
			want:   `got scheme "grpc"`,
		},
		"endpoint without a host": {
			values: map[string]string{"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://:4317"},
			want:   "has no host",
		},
		"endpoint naming an OTLP/HTTP path": {
			values: map[string]string{"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.example:4318/v1/traces"},
			want:   "origin only",
		},
		"endpoint with a query": {
			values: map[string]string{"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.example:4317?x=1"},
			want:   "origin only",
		},
	} {
		t.Run(name, func(t *testing.T) {
			destination, err := resolveTelemetryDestination(false, group(tc.values))
			require.Error(t, err)
			require.ErrorContains(t, err, tc.want)
			require.False(t, destination.Available(), "a refusal must never read as a usable destination")
			require.Empty(t, destination.AbsentReason, "a refusal must never read as an absent collector")
		})
	}
}

// The state decides. The cell's values override the module's local defaults one
// key at a time, so a deployed producer sees `available` and an endpoint from the
// cell beside the reason the local profile left behind. That is not a
// contradiction to refuse: the reason is ignored, and said so once.
func TestTelemetryDestinationAvailableIgnoresALeftoverReason(t *testing.T) {
	destination, err := resolveTelemetryDestination(false, group(map[string]string{
		"TELEMETRY_STATE":             "available",
		"OTEL_EXPORTER_OTLP_ENDPOINT": collector,
		"TELEMETRY_ABSENT_REASON":     "A local run has no cell collector.",
	}))
	require.NoError(t, err)
	require.True(t, destination.Available())
	require.Equal(t, collector, destination.URL)
	require.Empty(t, destination.AbsentReason, "a leftover reason must not turn an available collector into an absent one")
	require.Equal(t, "TELEMETRY_ABSENT_REASON", destination.Ignored)
	notice := destination.IgnoredNotice()
	require.Contains(t, notice, "TELEMETRY_ABSENT_REASON is set but TELEMETRY_STATE is `available`")
	require.NotContains(t, notice, "A local run", "the notice names the key, never its free-text value")
}

// ... and the other way round: a cell that says it has no collector is not
// refused for an endpoint a lower layer left behind, and that endpoint is not
// even parsed.
func TestTelemetryDestinationAbsentIgnoresALeftoverEndpoint(t *testing.T) {
	for name, leftover := range map[string]string{
		"a usable endpoint":       collector,
		"a malformed endpoint":    "otel-collector.ns.svc:4317",
		"an endpoint with a user": "http://user:hunter2@collector.example:4317",
		"an unreadable endpoint":  "http://[::1",
	} {
		t.Run(name, func(t *testing.T) {
			destination, err := resolveTelemetryDestination(false, group(map[string]string{
				"TELEMETRY_STATE":             "absent",
				"TELEMETRY_ABSENT_REASON":     "This cell has no ops cell.",
				"OTEL_EXPORTER_OTLP_ENDPOINT": leftover,
			}))
			require.NoError(t, err)
			require.False(t, destination.Available(), "a leftover endpoint must not turn an absent collector into an available one")
			require.Empty(t, destination.URL)
			require.Empty(t, destination.Endpoint)
			require.Equal(t, "This cell has no ops cell.", destination.AbsentReason)
			require.Equal(t, "OTEL_EXPORTER_OTLP_ENDPOINT", destination.Ignored)
			notice := destination.IgnoredNotice()
			require.Contains(t, notice, "OTEL_EXPORTER_OTLP_ENDPOINT is set but TELEMETRY_STATE is `absent`")
			require.NotContains(t, notice, "hunter2", "the notice names the key, never its value")
		})
	}
}

// A blank leftover is nothing left over.
func TestTelemetryDestinationBlankLeftoversAreNotNoticed(t *testing.T) {
	available, err := resolveTelemetryDestination(false, group(map[string]string{
		"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": collector, "TELEMETRY_ABSENT_REASON": " \n ",
	}))
	require.NoError(t, err)
	require.Empty(t, available.IgnoredNotice())

	absent, err := resolveTelemetryDestination(false, group(map[string]string{
		"TELEMETRY_STATE": "absent", "TELEMETRY_ABSENT_REASON": "no collector", "OTEL_EXPORTER_OTLP_ENDPOINT": "   ",
	}))
	require.NoError(t, err)
	require.Empty(t, absent.IgnoredNotice())
}

// layered is what the platform hands a workload: each layer overrides the one
// below it key by key, so a key a higher layer does not set survives from the
// lower one.
func layered(layers ...map[string]string) func(string) string {
	merged := map[string]string{}
	for _, layer := range layers {
		for key, value := range layer {
			merged[key] = value
		}
	}
	return group(merged)
}

// The case this exists for: the module's local profile defaults to `absent` with
// a reason, and a deployed cell overrides the state and the endpoint but not the
// reason. The cell's answer wins, whichever way it points.
func TestTelemetryDestinationTheCellOverridesTheModuleDefaults(t *testing.T) {
	moduleDefault := map[string]string{
		"TELEMETRY_STATE":         "absent",
		"TELEMETRY_ABSENT_REASON": "A local run has no cell collector.",
	}

	t.Run("a cell with a collector overrides the local absent default", func(t *testing.T) {
		destination, err := resolveTelemetryDestination(false, layered(moduleDefault, map[string]string{
			"TELEMETRY_STATE":             "available",
			"OTEL_EXPORTER_OTLP_ENDPOINT": collector,
		}))
		require.NoError(t, err)
		require.True(t, destination.Available())
		require.Equal(t, collector, destination.URL)
		require.Empty(t, destination.AbsentReason)
		require.Equal(t, "TELEMETRY_ABSENT_REASON", destination.Ignored)
	})

	t.Run("a cell with no collector, over a default that had an endpoint, is absent", func(t *testing.T) {
		destination, err := resolveTelemetryDestination(false, layered(
			map[string]string{"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": collector},
			map[string]string{"TELEMETRY_STATE": "absent", "TELEMETRY_ABSENT_REASON": "This cell has no ops cell."},
		))
		require.NoError(t, err)
		require.False(t, destination.Available())
		require.Equal(t, "This cell has no ops cell.", destination.AbsentReason)
		require.Equal(t, "OTEL_EXPORTER_OTLP_ENDPOINT", destination.Ignored)
	})

	t.Run("a cell that overrides only the state still has to supply what it requires", func(t *testing.T) {
		_, err := resolveTelemetryDestination(false, layered(moduleDefault, map[string]string{"TELEMETRY_STATE": "available"}))
		require.ErrorContains(t, err, "OTEL_EXPORTER_OTLP_ENDPOINT is not set",
			"the module default's reason is not an endpoint")

		_, err = resolveTelemetryDestination(false, layered(
			map[string]string{"TELEMETRY_STATE": "available", "OTEL_EXPORTER_OTLP_ENDPOINT": collector},
			map[string]string{"TELEMETRY_STATE": "absent"},
		))
		require.ErrorContains(t, err, "TELEMETRY_ABSENT_REASON is not set",
			"the module default's endpoint is not a reason")
	})
}

// A refusal quotes no part of an endpoint, which may carry credentials.
func TestTelemetryDestinationRefusalDoesNotEchoCredentials(t *testing.T) {
	_, err := resolveTelemetryDestination(false, group(map[string]string{
		"TELEMETRY_STATE":             "available",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://user:hunter2@collector.example:4317",
	}))
	require.ErrorContains(t, err, "must not carry credentials")
	require.NotContains(t, err.Error(), "hunter2")
}

// The group is read through the SDK's workspace-configuration lookup, by the
// names the platform delivers them under. A bare process variable of the same
// name is not the cell's answer.
func TestConfiguredTelemetryDestinationReadsTheObservabilityGroup(t *testing.T) {
	const prefix = "CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__"
	for _, key := range []string{"TELEMETRY_STATE", "OTEL_EXPORTER_OTLP_ENDPOINT", "TELEMETRY_ABSENT_REASON"} {
		t.Setenv(key, "")
		t.Setenv(prefix+key, "")
	}

	localDestination, err := configuredTelemetryDestination(true)
	require.NoError(t, err)
	require.False(t, localDestination.Available())
	require.NotEmpty(t, localDestination.AbsentReason)
	_, err = configuredTelemetryDestination(false)
	require.ErrorContains(t, err, "TELEMETRY_STATE is not set")

	// Ambient variables a shell or a pod may carry are not the group.
	t.Setenv("TELEMETRY_STATE", "available")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://ambient.example:4318")
	_, err = configuredTelemetryDestination(false)
	require.ErrorContains(t, err, "TELEMETRY_STATE is not set")
	t.Setenv("TELEMETRY_STATE", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	t.Setenv(prefix+"TELEMETRY_STATE", "available")
	t.Setenv(prefix+"OTEL_EXPORTER_OTLP_ENDPOINT", collector)
	destination, err := configuredTelemetryDestination(false)
	require.NoError(t, err)
	require.Equal(t, "otel-collector.otel-collector.svc.cluster.local:4317", destination.Endpoint)
	require.True(t, destination.Insecure)

	t.Setenv(prefix+"TELEMETRY_STATE", "absent")
	t.Setenv(prefix+"OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv(prefix+"TELEMETRY_ABSENT_REASON", "a local run has no cell collector")
	destination, err = configuredTelemetryDestination(false)
	require.NoError(t, err)
	require.False(t, destination.Available())
	require.Equal(t, "a local run has no cell collector", destination.AbsentReason)
}

// Through the SDK's lookup, with the keys exactly as a deployed cell delivers
// them after the layers merge: the cell's state and endpoint, and the reason the
// module's local profile left behind.
func TestConfiguredTelemetryDestinationStateDecidesAcrossLayers(t *testing.T) {
	const prefix = "CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__"
	t.Setenv(prefix+"TELEMETRY_STATE", "available")
	t.Setenv(prefix+"OTEL_EXPORTER_OTLP_ENDPOINT", collector)
	t.Setenv(prefix+"TELEMETRY_ABSENT_REASON", "A local run has no cell collector.")

	destination, err := configuredTelemetryDestination(false)
	require.NoError(t, err)
	require.True(t, destination.Available())
	require.Equal(t, collector, destination.URL)
	require.NotEmpty(t, destination.IgnoredNotice())
}

// Configuration that cannot work is refused before the process acquires
// anything: a group that did not arrive is not a cell without a collector.
// Only a local runtime can infer an absent collector from the missing state.
func TestStartupRefusesAnObservabilityGroupThatDidNotArrive(t *testing.T) {
	const prefix = "CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__"
	for _, key := range []string{"TELEMETRY_STATE", "OTEL_EXPORTER_OTLP_ENDPOINT", "TELEMETRY_ABSENT_REASON"} {
		t.Setenv(prefix+key, "")
	}
	// The key-custody requirement answers first outside a local run; satisfy it
	// so the observability requirement is what is under test.
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__VAULT__VAULT_KEY_CUSTODY", "seed-signing-key")
	require.NoError(t, requireStartupConfiguration(true))
	require.ErrorContains(t, requireStartupConfiguration(false), "TELEMETRY_STATE is not set")

	t.Setenv(prefix+"TELEMETRY_STATE", "absent")
	t.Setenv(prefix+"TELEMETRY_ABSENT_REASON", "a local run has no cell collector")
	require.NoError(t, requireStartupConfiguration(true))
	require.NoError(t, requireStartupConfiguration(false))
}

// service.name is never a literal typed here: the environment wins, then the
// service's own Codefly identity.
func TestTelemetryResourceServiceName(t *testing.T) {
	resolve := func(t *testing.T, identity string) string {
		t.Helper()
		res, err := telemetryResource(t.Context(), identity)
		require.NoError(t, err)
		return telemetryServiceName(res)
	}

	t.Run("falls back to the Codefly service identity", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
		require.Equal(t, "saas-starter/accounts", resolve(t, "saas-starter/accounts"))
	})
	t.Run("OTEL_SERVICE_NAME wins over the identity", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "named-by-the-platform")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
		require.Equal(t, "named-by-the-platform", resolve(t, "saas-starter/accounts"))
	})
	t.Run("OTEL_RESOURCE_ATTRIBUTES can set it", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=attributed-by-the-platform,deployment.environment=test")
		require.Equal(t, "attributed-by-the-platform", resolve(t, "saas-starter/accounts"))
	})
	t.Run("without either, the SDK default rather than a literal", func(t *testing.T) {
		t.Setenv("OTEL_SERVICE_NAME", "")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
		require.True(t, strings.HasPrefix(resolve(t, ""), "unknown_service"), resolve(t, ""))
	})
}

// The identity is the one the SDK built its provider with, the same one wool
// stamps on this process's log lines — read from the provider, not from the
// runtime's environment carrier.
func TestCodeflyServiceNameIsTheSDKProviderIdentity(t *testing.T) {
	carrying := func(unique string) context.Context {
		return wool.New(t.Context(), &wool.Resource{Kind: "service", Unique: unique}).Inject(t.Context())
	}
	require.Equal(t, "saas-starter/accounts", codeflyServiceName(carrying("saas-starter/accounts")))
	require.Equal(t, "accounts", codeflyServiceName(carrying("/accounts")), "an identity with no module")
	require.Empty(t, codeflyServiceName(carrying("/")), "an identity with neither")
	require.Empty(t, codeflyServiceName(t.Context()), "no provider: the SDK was not initialised")
}

// wool's tracer speaks plaintext only. An https:// endpoint is refused rather
// than dialed in the clear and reported as TLS.
func TestEnableTracingRefusesATransportWoolCannotDial(t *testing.T) {
	provider, err := enableTracing(t.Context(),
		telemetryDestination{Endpoint: "collector.example:4317", Insecure: false}, false)
	require.ErrorIs(t, err, errWoolCannotDialTLS)
	require.Nil(t, provider)
}

func TestEnableTracingRegistersOnlyWhatTheDestinationCalls(t *testing.T) {
	restore := func(t *testing.T) {
		t.Helper()
		previous := otel.GetTracerProvider()
		t.Cleanup(func() {
			wool.RegisterTelemetry(nil)
			otel.SetTracerProvider(previous)
		})
	}
	absent := telemetryDestination{AbsentReason: "a local run has no cell collector"}

	t.Run("absent outside a local run registers no tracer", func(t *testing.T) {
		restore(t)
		provider, err := enableTracing(t.Context(), absent, false)
		require.NoError(t, err)
		require.Nil(t, provider)
		require.False(t, wool.TelemetryEnabled())
	})
	t.Run("absent in a local run uses wool's stdout tracer", func(t *testing.T) {
		restore(t)
		provider, err := enableTracing(t.Context(), absent, true)
		require.NoError(t, err)
		require.NotNil(t, provider)
		require.True(t, wool.TelemetryEnabled())
		shutdown(t, provider)
	})
	t.Run("available over http exports spans to the collector, named by the environment", func(t *testing.T) {
		restore(t)
		t.Setenv("OTEL_SERVICE_NAME", "named-by-the-platform")
		t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "")
		endpoint, capture := startTraceCapture(t)
		provider, err := enableTracing(t.Context(), telemetryDestination{Endpoint: endpoint, Insecure: true}, false)
		require.NoError(t, err)
		require.NotNil(t, provider)
		require.True(t, wool.TelemetryEnabled())

		_, span := otel.Tracer("test").Start(t.Context(), "operation")
		span.End()
		shutdown(t, provider) // flushes the batch

		select {
		case request := <-capture.requests:
			var name string
			for _, resourceSpans := range request.GetResourceSpans() {
				for _, attribute := range resourceSpans.GetResource().GetAttributes() {
					if attribute.GetKey() == "service.name" {
						name = attribute.GetValue().GetStringValue()
					}
				}
			}
			require.Equal(t, "named-by-the-platform", name)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for OTLP spans")
		}
	})
}

type traceCapture struct {
	collectortracev1.UnimplementedTraceServiceServer
	requests chan *collectortracev1.ExportTraceServiceRequest
}

func (c *traceCapture) Export(
	_ context.Context,
	request *collectortracev1.ExportTraceServiceRequest,
) (*collectortracev1.ExportTraceServiceResponse, error) {
	c.requests <- request
	return &collectortracev1.ExportTraceServiceResponse{}, nil
}

// startTraceCapture serves an in-process OTLP/gRPC trace receiver on loopback.
func startTraceCapture(t *testing.T) (string, *traceCapture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	capture := &traceCapture{requests: make(chan *collectortracev1.ExportTraceServiceRequest, 4)}
	server := grpc.NewServer()
	collectortracev1.RegisterTraceServiceServer(server, capture)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String(), capture
}

func shutdown(t *testing.T, provider interface{ Shutdown(context.Context) error }) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, provider.Shutdown(ctx))
}
