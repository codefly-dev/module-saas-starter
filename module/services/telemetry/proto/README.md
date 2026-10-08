# Telemetry protobuf input

The telemetry gateway implements the standard OpenTelemetry Protocol services
from `go.opentelemetry.io/proto/otlp`; it does not duplicate those upstream
service definitions.

The local `SchemaMarker` is deliberately code-generation-free. It keeps the
Codefly Go gRPC agent's required protobuf sync input valid for a service that
depends exclusively on upstream wire schemas.
