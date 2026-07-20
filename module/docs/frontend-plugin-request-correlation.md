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
The BFF forwards it only when it is a structurally valid version-`00` value with
non-zero trace and parent IDs. Malformed values are dropped. `tracestate`,
`baggage`, and vendor trace headers are not forwarded in the v1 contract. Trace
flags inside an accepted `traceparent` remain untrusted propagation metadata.

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
