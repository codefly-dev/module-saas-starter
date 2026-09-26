# Scoped audit metrics

`AuditService.AggregateAuditLog` is the sole aggregation backend. Graph event
names are the exact registry names; schema major versions are separate metadata.
The runtime does not append `.v1`. All requests still require organization audit
read authority. Resource/collection filters additionally require current read
access; possessing `audit:read` does not bypass that check.

## Configuration recipes

The SDK supplies `orgId`, `from`, and `to` through `runDashboard`'s viewer context.
Both time endpoints are inclusive; reversed windows fail. A source metric can use:

```json
{
  "id": "completed_jobs",
  "kind": "source",
  "filter": {
    "event": "sync_done",
    "resource": "datasource",
    "resourceId": "connected-source-id"
  },
  "groupBy": "event_type",
  "aggregation": "count_distinct",
  "field": "payload:job_id"
}
```

Declare `sync_done` as `saas.datasource.sync.completed`. `resourceId` is the
connected source ID, not a collection node ID. Accounts loads that source under
its organization, resolves its **current** `BoundaryNodeID`, and checks the
viewer's current `documents:read` grant through an exact node lookup in the
same transaction. The lookup shares its predicates with scope listing. Existing
and newly connected sources use identical logic. Missing sources/bindings, a
changed binding the viewer cannot read, and revoked grants deny the read. There
is no registration migration or copied permission engine.

For a document collection use `filter: {event: "ingested", collectionId:
"collection-node-id"}` with event `saas.document.ingested`. Accounts checks the
same current collection grant and adds `payload_contains.boundary` itself.
Conflicting boundary predicates fail. Only registered `saas.document.*` events
with a boundary field support this filter. Optional `payloadContains` string
predicates (such as `version` or `correlation_id`) are ANDed with it. An additional
`resourceId` requires its own registered-record read permission as well.
Naming a collection through `payloadContains.boundary` on one of those events is
the same read as `collectionId` and takes the same grant check: accounts
promotes the predicate into the scoped path, so the two spellings of one filter
cannot authorize differently.

This check scopes a filter; it does not make collection activity confidential
from the organization. Organization-wide aggregation remains an organization
audit read, so a member with organization audit authority can still group by
`payload:boundary` and see per-collection counts without holding any collection
grant. Do not build an access-control boundary on `collectionId` alone.

See [the configuration-only graph](./examples/scoped-audit-dashboard.json).
Replace the two placeholder IDs with the selected source and collection IDs.
The graph uses the existing shared dashboard renderer and requires no aggregation
handler in a consuming solution. Both the SDK graph and host dashboard DSL accept
`resource`, `resourceId`, `collectionId`, and `payloadContains`.

## Producer contract and availability

Accounts dispatch/validation events use row `resource="datasource"` and
`resource_id=source.ID`. Ingestion workers emit through
`ModuleCapabilitiesService.EmitAuditEvent`, authenticated by a module Work
Context and the current tenant-bound `MODULE_PRINCIPALS` declaration. That RPC
writes row `resource=scope.solution`, `resource_id=scope.entry_id`, and payload
`solution=scope.solution`; the actor is the attributed actor supplied by the
emitter. Therefore source completion/failure producers must send
`scope.solution="datasource"`, `scope.entry_id=<source-id>`. A different identity
does not silently match this source filter. Document producers must send the
actual collection node in `fields.boundary`, with the document entry as entry_id.
No consumer-specific identity is built into the module.

Registered source completion/failure fields are `job_id`, `processed`,
`new_versions`, `deleted`, `failed`, `attempt`, `retryable`, plus source/provenance
fields. Count distinct `job_id` for logical completed/affected jobs; use count
only when measuring attempts. Failed attempts may later complete. Dispatch is
not completed ingestion. `processed` and `new_versions` do not establish index
readiness. Summing per-attempt counts is not a deduplicated document total.

Every document producer event carries a `scope.solution` of at least one
character; the RPC refuses one without it (`min_len: 1` on the request field),
and the scope value overwrites any `solution` the payload carried. `entry_id` is
**not** constrained: it carries only a maximum length, because the same RPC
serves entry-less events such as `saas.document.search`. A governance event
naming its entry is therefore the producer's contract, not something the host
enforces — a row whose `resource_id` is empty is accepted.

Governance actions a document producer takes on an entry each have their own
type:

| Type | Recorded when | Payload beyond `solution` |
| --- | --- | --- |
| `saas.document.ownership_transferred` | an entry's owner is reassigned | `new_owner_subject_id` |
| `saas.document.frozen` | an entry is put under boundary governance | — |
| `saas.document.unfrozen` | an entry is released from boundary governance | — |
| `saas.document.governance_refused` | any of the above, or a quarantine release, is refused and commits nothing | `action`, `reason` (both required, nonempty), `claimed_tenant` |

A refusal is never an `outcome` on the action's own type. It commits nothing, so
it is an observation rather than a privileged write, and one type carries one
durability; recording it as a payload value on some types and a type name on
others would also leave "was this refused?" with no single query predicate.
Every refusal is one observational type discriminated by `action`, so
`event_type = saas.document.governance_refused` finds every refused governance
action — including ones added later. `claimed_tenant` (the tenant a
quarantine-release approval named when the release was delivered for another) is
export-stripped like a PII field: it identifies a party other than the tenant
whose trail the row sits on, so it stays out of the customer's S3 destination,
the JSON download and the webhook fan-out, and correlating it is the host's job.

Freeze and unfreeze are registered as a pair. The trail is append-only and
`type` is immutable, so a type for entering a governed state with none for
leaving it stops answering "which entries were governed as of date D" the first
time a producer lifts one.

What belongs on the spine is who did what to which resource, not how an ingest
ran. The registry, not this page, is the inventory of which types exist.

`saas.document.read` and `saas.document.search` are observational version-1
contracts. `boundary`, `correlation_id`, and `outcome` are required nonempty
strings; measurements remain optional. In addition to document provenance they accept:

| Field | Meaning |
| --- | --- |
| `boundary` | Authoritative collection node ID |
| `correlation_id` | Stable logical operation identity across transport retries |
| `outcome` | `returned`, `empty`, `denied`, or `failed` |
| `result_count` | Observed returned entries/results; zero only if observed |
| `duration_ms` | Observed elapsed milliseconds |

Emit a separate event for each collection involved in a multi-collection search,
with the same correlation ID and only that collection's counts. Use a stable
per-collection emission idempotency key to collapse transport retries. These
records describe evidence access, not answer success. Their registration alone
does not prove an installed producer emits them. Never include raw queries,
prompts, contents, excerpts, credentials, or provider responses. Unknown payload
fields are rejected by the module emission API.

Answered/refused/failed query outcomes, citation counts, token usage, index-ready
and committed-document counts still require confirmed producer events. Do not
substitute these read events or source dispatch for them. A complete aggregation
cannot prove that an emitter has reported every operation.

## Version acknowledgement

Successful aggregate responses include `scope_contract_version: 1` (Connect JSON:
`scopeContractVersion`), including empty results. SDK graphs, host dashboard hooks
and imperative previews require this acknowledgement whenever `resourceId`,
`collectionId`, or nonempty `payloadContains` is used. Missing or unsupported
versions fail with a contract error: an older server may ignore unknown request
fields and return organization-wide counts, so even an empty response cannot be
accepted without acknowledgement. Upgrade the server before scoped clients.

SDK facade metadata and bindings are generated from the same exported contract
snapshot under `contracts/api/accounts/connect`; unpublished accounts protos are
never the SDK binding input. Regenerate the export before generating the SDK.

## Unknown and partial results

Empty queries have no points and render **No data yet**. Numeric sums with no
numeric observations omit their value, just like averages/percentiles; an emitted
zero stays zero. `samples[alias]` reports observations before reduction (so
repeated logical IDs are not mistaken for missing telemetry). Fewer samples than
events means partial telemetry. Older servers without samples cannot establish
numeric completeness.

SDK series expose `coverage: complete | partial | empty` and nullable `total`.
Missing derived operands and zero denominators stay unknown. Partial series keep
observed chart points with a visible label and withhold the total. Distinct
counts, averages, percentiles and ratios have no scalar total across multiple
groups; summing or averaging those summaries would misrepresent the result.
Sums and differences of complete additive inputs retain additive totals through
nested derived metrics.
The host DSL also withholds totals after top-N truncation. Authorization/RPC
errors remain errors, never empty successful results. Existing consumers of the
SDK must handle `total: null` when adopting this contract.

## Independent validation

Run `go test -race ./internal/auditmetricstest` from accounts/code for pure
resource authorization tests. Set `AUDIT_METRICS_TEST_DSN` to an **independent,
disposable PostgreSQL database** to execute the real SQL filtering, retry dedupe,
samples, empty/zero and ratio tests. The tests use rolled-back fixtures and a non-owner role with the shipped audit
RLS policy; they never start or manage an application stack. The authorization CI
gate provisions its own PostgreSQL service and requires these tests (a missing
DSN with AUDIT_METRICS_REQUIRE_DB=1 in that gate is an error). Frontend package tests cover
exact catalog names, compilation, unknown outcomes and partial rendering.
