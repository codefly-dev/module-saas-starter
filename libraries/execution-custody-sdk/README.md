# Execution custody SDK

Accounts-owned Go client and wire contract for the private execution custody v1
broker. Import `github.com/codefly-dev/module-saas-starter/libraries/execution-custody-sdk/go`
at a reviewed commit/pseudo-version, as with the source-read SDK. This standalone
module imports canonical Work Context protobuf types, never service implementation,
issuer, storage, or policy packages. It has no browser entry point.

`NewClient(origin, transport)` requires a bare HTTPS origin and a transport with
TLS 1.3 or newer, normal certificate verification, no proxy and no custom peer
verification, TLS dialing or alternate protocol callbacks. The client clones the transport, never follows redirects,
and sends one POST per call without retry headers or retry logic. Every request
and complete response is bounded by five seconds or the caller's earlier deadline.
Call `Close` when retiring a client to close its idle connections.

This intentionally retains the stricter consumer transport and JSON rules: older
callers of the service-local client must set `TLSClientConfig.MinVersion` to
`tls.VersionTLS13`, remove proxy/custom verification, TLS dialing or alternate
protocol callbacks, and accept only
known v1 response fields. The broker already requires TLS 1.3. There is no HTTP,
skip-verification, proxy, redirect, or retry compatibility mode.

- `Register(ctx, ownerAccessToken, RegisterRequest)` sends the original parent
  token and immutable admission binding to the owner broker. An uncertain result
  does not authorize a different admission or horizon.
- `Recover(ctx, currentOwnerAccessToken, RecoverRequest)` only reads the exact
  existing registration. It carries no parent token; absence never triggers a
  registration fallback. Lost acknowledgement recovery returns the original child.
- `Exchange(ctx, ExchangeRequest)` carries no owner bearer. The configured mTLS
  worker identity and exact sealed binding are checked by the broker. `Lookup`
  selects the existing read-only attenuation; it cannot renew the original horizon.

Successful responses and typed errors must be complete, bounded v1 JSON without
unknown fields. Unknown codes or mismatched HTTP status/code pairs become `Unavailable` (including
redirects that claim a caller denial). `Error.HTTPStatus()` supplies the same v1
status mapping to the broker and client; credential-bearing
response bodies and transport errors never appear in returned errors. Adding wire
fields requires an explicit compatible contract/client release decision.

`ClaimsDigest` implements `wc-claims-proto-v1` over deterministic canonical
`WorkContextV1` protobuf bytes and rejects unknown fields at every current nested
actor/scope position. The digest is not signature, version, expiry, scope or current
revision verification. Callers must use the canonical Work Context verifier, then
check lineage, audience, scopes, profile and sealed horizon. Authority and policy
remain exclusively with Accounts; consumer-owned admission journals keep only
non-secret intent, references and verified binding metadata.

Run `go test -race ./...` and `go vet ./...` from `go/`. Transport/digest conformance
uses local TLS fixtures. The Accounts broker qualification additionally exercises
actual persisted registration, current revision/revocation, signing/Vault rotation,
and process restart with this same client. No broker or deployment is created by
importing this library.
