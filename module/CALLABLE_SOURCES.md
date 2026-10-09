# Callable sources

The host owns OAuth credentials and outbound calls. A consuming solution imports
its manifest declarations through `DeclareSourceOperations`; a person connects
the source. There is no route-entry form, arbitrary-URL proxy, second credential
store, background call queue or per-organization provider-app registry.

## Wire and authority

The accounts **Connect endpoint** serves `DatasourceService.InvokeSourceOperation`
and `LookupInvokeSourceOperation`, including Connect JSON, gRPC and gRPC-Web.
The method is marked `codefly.runnable.v0.operation`; `tool` is absent. Discovery
projects individual declaration schemas/effects/digests over this one binding.
The caller never chooses a destination, HTTP header, credential or method.

Invoke takes `org_id`, `source_id`, `operation`, `input_json`, and `effect_id`.
`input_json` is a string containing exactly one JSON object. The complete
protojson request (proto field names, unpopulated fields included) is limited to
65,536 bytes. Declarations require a closed input-object schema; named template
parameters become escaped path components, listed query fields become query
parameters, and the remaining object becomes the body. GET and DELETE refuse a
body. Remote schema references and schema loaders are disabled.

Declarations bound provider output to at most 65,536 bytes. The response carries
`output_json` and a receipt with `effect_id`, RFC3339Nano `committed_at`,
`status`, numeric `provider_status`, and the same `output_json`. Both JSON strings
contain the validated output object. The full encoded response is limited to
1 MiB, including both copies and escaping overhead. Unknown outcomes use `{}` in both
output strings and `receipt.status: "unknown"`.

The effect ID is limited to the SDK's 128-byte bound and may be supplied in `X-Codefly-Effect-Id` or `Idempotency-Key` or the
request field. Multiple headers or conflicting values are refused. Equivalent
header/field carriers are normalized before hashing. Other request fields,
including the exact JSON text, are part of the SDK request digest: different
whitespace is a different request. Use the same request when recovering.

Interactive callers pass organization membership and the source-boundary role
gate. Work Context callers present the existing delegated-audience exchange's
`saas-datasource` capability with an explicit `datasource.sources` source scope.
The `source` scope slot requires invoke/read and read-only lookup for the same
IDs. The host checks current delegation/binding/revision, owner membership,
boundary permission, personal ownership and current declaration, including on
replay. The lookup request contains only `effect_id`; its tenant and source
binding are recovered from verified identity and durable attempt metadata.

## Receipts and uncertain outcomes

The SDK receipts guard serializes `(tenant, effect_id, method)` and verifies the
request digest. Its Postgres reads use the request pool under tenant RLS.
`receipts.Record` writes the full response using the exact org transaction that
commits the host result. The receipt SQL adapter reserves pool capacity for that
transaction while the SDK serialization hold has a connection.

Before sending HTTP, the host commits an attempt marker containing only actor,
source, operation, declaration digest and request digest. A committed receipt
replays without a provider request. An effect with a different input is refused.
The attempt marker survives process death, a lost mutation reply, a provider
5xx, an invalid mutation output or failure to commit the local result. It never
expires automatically: a remote mutation cannot be rolled back with Postgres.
Lookup reports unknown when that marker has no committed receipt and never
contacts the provider. Missing effects return NotFound.

A trustworthy provider 4xx refusal clears the attempt and reports
FailedPrecondition; 429 clears it and returns the same `DATASOURCE_RATE_LIMITED`
ErrorInfo/RetryInfo shape as FetchDatasourceFiles. Read-only transient failures
can be retried. Unknown mutations use `SOURCE_OPERATION_OUTCOME_UNKNOWN`, a
non-retryable FailedPrecondition; using the same effect again still cannot send
another request. A new effect is a new authorized call, not recovery.

Calls share the existing credential budget Scheduler with API sync and use
PriorityInteractive. New source configurations persist a deployment-keyed HMAC
budget identity, without storing the credential in the budget key. Legacy API
sources retain their source-scoped fallback until reconnected.

## OAuth and disclosure

`client_credentials` keeps the client secret, cached access token and expiry in
the existing datasourceCipher envelope. Authorization-code linking uses the
existing datasourceAccountLinker seam, PKCE S256 and committed one-time state
bound to organization/person/source/connector/redirect. The deployment-wide
`datasource-oauth` configuration group provides app metadata, client IDs and
secrets. This generic link identifies a source authorization, not a provider
subject or email; it is not a provider-directory ACL mapping.

Refresh and client-credentials resolution reuse the credential row lock.
Transport uses the existing resolved-IP guard and same-origin redirect policy.
Named refusal/receipt/audit constructors discard provider error bodies and raw
transport errors. Credential reflection in a successful provider body is refused.
Audit records actor, source, operation, effect, effect ID and the closed outcome
vocabulary (`committed`, `refused`, `unresolved`, `rate_limited`), never input,
output, tokens or URLs. Failed operation resolution uses an `UNKNOWN` effect.

## Generation and qualification

Core's option import closure is copied byte for byte under the accounts proto's
`vendor/` module and mapped to Core's registered Go packages. Generate only the
owned `saas` tree, then run business/adapters/catalog generation, contract export,
the frontend SDK script, and `codefly generate runnables saas-starter`. CI runs
`codefly generate runnables saas-starter --check` alongside contract drift checks.

The operations conformance suite is independent of sync conformance. Its
in-memory and httptest fixtures qualify HTTP and host decisions; real business
tests require Postgres and Vault booted by Codefly. Test results and any boot or
Core derivation blocker are recorded in the PR; a compiled test is not a passed
integration test. Release stays gated on acceptance of handbook proposal #269.
