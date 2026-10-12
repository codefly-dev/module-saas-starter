# Frontend plugin request correlation

Status: active (`FP-034`)
Applies to: every request through the generic frontend plugin BFF

The host-owned `x-request-id` is the sole public support correlation value for
plugin traffic. It lets a user report one failed attempt without exposing a
token, tenant, endpoint, trace, backend error, or internal log identifier. It is
diagnostic metadata only: it never grants authority, selects a tenant, changes
idempotency, keys a cache, or makes retries equivalent.

## Request ID lifecycle

For every BFF attempt, including requests rejected before target resolution:

1. The BFF generates a fresh opaque request ID. The current implementation uses
   `crypto.randomUUID()`; consumers must treat the value as opaque.
2. Caller-supplied `x-request-id`, `x-correlation-id`, `request-id`, and other
   `x-*` values are ignored. They cannot choose the support identifier.
3. The BFF forwards its generated value to the resolved backend as
   `x-request-id` alongside the validated bearer and safe transport headers.
4. Backend-supplied request/correlation headers are ignored. The host value
   remains authoritative on the browser response.
5. Every response carries that value in `x-request-id`. Host-generated
   `application/problem+json` bodies repeat the same value as `requestId`.
6. A retry is a new attempt and receives a new request ID. Capability-handshake
   failures are not cached, so a boundary retry also receives a new value.

The public React failure mapper accepts a request ID only from the BFF response
header and only when it matches the bounded safe-character contract. It never
falls back to `requestId` embedded in a product/backend body. The host error
boundary may display the validated value, but product error text and private
identifiers remain hidden.

## Trace context is separate

`traceparent` is W3C propagation metadata, not the public support identifier.
One parser serves every boundary in the frontend that forwards it —
`src/lib/trace-context.ts` — so the same header never gets two answers
depending on which boundary a caller reaches. It is strict: the value must be a
structurally valid `traceparent` with non-zero trace and parent IDs, spelled in
lowercase hex as the specification requires. Malformed values are dropped,
never repaired, and nothing is lowercased into validity. A later version is
relayed **as sent**, including its flag bits and its uninterpreted trailing
fields: these boundaries start no span and assign no parent ID, so they have no
version of their own to advertise and rewriting one as `00` would assert a
context nobody produced.

`baggage` and vendor trace headers are forwarded by no boundary. `tracestate`
differs by boundary, and deliberately:

| Boundary | `traceparent` | `tracestate` |
| --- | --- | --- |
| Plugin BFF (`server/plugin-bff.ts`) | forwarded | **not** forwarded — the frozen v1 contract |
| Solution proxy (`src/app/api/solutions/[id]/proxy/[...path]`) | forwarded | forwarded when the caller presented a credential: validated member syntax, unique keys, truncated from the right past 32 members or 512 bytes |

### The sampled flag is an instruction, not metadata

Trace flags inside an accepted `traceparent` remain untrusted propagation
metadata for every purpose below — but the sampled bit is also an *instruction*
to the upstream's sampler, which OpenTelemetry's default `ParentBased` sampler
obeys. A boundary therefore forwards it only from a caller that presented a
credential the upstream will verify:

- The plugin BFF rejects anything without a syntactically valid `Bearer` before
  it builds upstream headers, so every `traceparent` it forwards came from a
  credentialled caller.
- The solution proxy admits a request carrying neither `Origin` nor
  `Sec-Fetch-Site`, and serves a solution's `.well-known` anonymously, on a
  request it has already stamped with the internal trust token. For a caller
  that presented no credential at all it **clears the sampled bit in place** and
  withholds `tracestate` (several vendors re-state the sampling decision there,
  OpenTelemetry's `ot=` entry among them). The trace still links — trace ID and
  parent ID travel, so spans join — and only the instruction is withheld, so no
  unauthenticated caller can force recording and export on the gateway or on a
  solution behind it.

A valid incoming `traceparent` is still caller-controlled and must never be
used for authentication, authorization, tenancy, rate-limit identity,
idempotency, or support lookup without an independently authenticated log
context. It is not returned to the browser or rendered by the plugin boundary.
FP-035 owns actual spans, metrics, and structured transport events; FP-034 only
freezes safe correlation transport.

## Backend and operations obligations

Product gateways/backends should attach the received host `x-request-id` to
their structured request context and logs after normal log sanitization. They
may generate additional private span or operation IDs, but must not ask the
browser to present those values and must not overwrite the host correlation
header as authority.

Support evidence records only the host request ID, stable problem code, status,
and coarse timestamp. It never records access or refresh tokens, cookies,
private Codefly endpoint values, tenant data, request/response bodies, internal
credentials, or raw exception messages.

This contract uses standard HTTP metadata and intentionally adds no duplicate
protobuf field. Product RPC payload schemas remain domain-owned; Connect and
REST use the same BFF-generated header.
