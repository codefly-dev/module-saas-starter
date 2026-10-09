# Callable sources

The host owns OAuth credentials and outbound calls. A consuming solution imports
its manifest declarations through `DeclareSourceOperations`; a person connects
the source. There is no route-entry form, arbitrary-URL proxy, second credential
store, background call queue or per-organization provider-app registry.

## Wire and authority

The accounts **Connect endpoint** serves `DatasourceService.InvokeSourceOperation`
and `LookupInvokeSourceOperation`, including Connect JSON, gRPC and gRPC-Web.
The method is marked `codefly.runnable.v0.operation`; `tool` is absent.
Install the `accounts/connect/InvokeSourceOperation` derived package: the
`authority` listener remains limited to the existing module authority methods.
The current contract exporter copies the whole service descriptor to both endpoints,
so it also derives an authority package that this listener cannot serve. That
export needs endpoint-specific projection; generated output is never filtered by
hand. Discovery projects individual declaration schemas/effects/digests over this one binding.
The caller never chooses a destination, HTTP header, credential or method.

Invoke takes `org_id`, `source_id`, `operation`, `input_json`, and `effect_id`.
`input_json` is a string containing exactly one JSON object. The complete
protojson request (proto field names, unpopulated fields included) is limited to
65,536 bytes. Declarations require a closed input-object schema; named template
parameters become escaped path components, listed query fields become query
parameters, and the remaining object becomes the body. GET and DELETE refuse a
body. The declared path is appended to the source base path, preserving escaped
segments. Remote schema references and schema loaders are disabled. Compiled
schema pairs are cached by the verified canonical declaration digest in a
bounded LRU; changing a declaration cannot reuse the old compiled contract.

Declarations bound provider output to at most 65,536 bytes. The response carries
`output_json` and a receipt with `effect_id`, RFC3339Nano `committed_at`,
`status`, numeric `provider_status`, and the same `output_json`. Both JSON strings
contain the validated output object. The full encoded response is limited to
1 MiB, including both copies and escaping overhead. Unknown outcomes use `{}` in both
output strings and `receipt.status: "unknown"`.

The effect ID is limited to the SDK's 128-byte bound and may be supplied in `X-Codefly-Effect-Id` or `Idempotency-Key` or the
request field. Empty or conflicting carriers are refused. Equivalent
header/field carriers, including matching headers, are normalized before hashing. Other request fields,
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
contacts the provider. NotFound `source effect not found` means no attempt was
recorded; a same-ID retry is safe. A read-only local commit failure returns
Unavailable and allows the same request to retry.

A trustworthy provider 4xx refusal clears the attempt and reports
FailedPrecondition; 429 clears it and returns the same `DATASOURCE_RATE_LIMITED`
ErrorInfo/RetryInfo shape as FetchDatasourceFiles. Read-only transient failures
can be retried. Unknown mutations use `SOURCE_OPERATION_OUTCOME_UNKNOWN`, a
non-retryable FailedPrecondition; using the same effect again still cannot send
another request. A new effect is a new authorized call, not recovery. Provider
refusals include their known HTTP code as decimal `provider_status` ErrorInfo
metadata. InvalidArgument input refusals include a BadRequest FieldViolation
whose `field` is the JSON pointer, without input values.

`PruneSourceOperationReceipts` and `LookupPruneSourceOperationReceipts` expose
receipt cleanup as a second host Runnable on Connect. Shared sources require an
organization administrator; a personal source requires its owning member. Both
need the selected source permission, including on replay. The composition
schedules its `accounts/connect/PruneSourceOperationReceipts` binding in the
runtime, resolving the same `source` slot; it has no `tool` exposure. A call
removes at most 1,000 responses. Schedule it frequently
enough to drain the source; a full batch means more may remain. There is no
host timer or queue. A host with no scheduled binding does not run retention.
The cutoff is Invoke's declared `total_timeout` plus a 24-hour lookup grace.
The operation deletes only that source's expired receipt responses (including
its own old cleanup receipts), with cleanup, audit and the new SDK receipt in
one tenant transaction. It retains compact effect markers permanently, including
unknown mutations: removing the last marker would permit an old mutation to run
again. After output expiry, mutation Lookup returns `unknown` with `{}` and the
same effect still cannot redispatch. A bounded retention window for opaque
caller-chosen effect IDs cannot remove this last marker while preserving that
guarantee.

Each outbound exchange disables connection reuse, so the HTTP transport cannot
transparently replay a method after losing a reused connection. The declaration
owns the effect even when a provider uses GET for a mutation.

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
secrets. This version accepts one registration, under `api`, and cannot shadow
a connector-owned linker. Authorization-code envelopes hold only provider
tokens and expiry; refresh resolves the client ID, secret, token URL and scopes
from the current deployment registration, so app rotation does not require
copying a new secret into every source. This generic link identifies a source authorization, not a provider
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
After changing the audit registry, run `go test -tags pure ./pkg/business -run
AuditEventsContribution -update` from accounts/code, then `go run
./cmd/module-package run-generators --module ..` from module/tools to regenerate
the event contribution and every composed catalog.

The operations conformance suite is independent of sync conformance. Its
in-memory and httptest fixtures qualify HTTP and host decisions; real business
tests require Postgres and Vault booted by Codefly. Test results and any boot or
Core/CLI delivery blocker are recorded in the PR; a compiled test is not a passed
integration test. The callable-sources handbook decision is accepted (proposal
#269, merged in handbook PR #271). Release still requires the Core release carrying
#755 and the CLI release carrying #943. Development qualification with CLI PR #947
preserves scope slots in operation documents and resolves composition selections
during preparation; the released pins must pass the same checks before readiness.
A passing generation drift check alone does not qualify the delivery path.
