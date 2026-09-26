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
emitter. `entry_id` is stored verbatim: it is a text column, so an entry id of
any shape (a ULID, a provider's object id, a content digest, a numeric id) is
the row's resource id. It must be an opaque identifier that carries neither
record content nor personal data, and that requirement is enforced rather than
asked for: it is not a payload field, so no PII redaction applies to it, it is
exported as written on every path, and the append-only table keeps it for the
whole retention period with no way to correct or erase it. A value carrying a
path separator, an `@`, whitespace or a control character is a locator rather
than an identifier, and the host drops it to no resource id — which is what the
writer used to do to it — without refusing the emit, so the event still lands.
A producer with a path, a filename or an address to record puts it in a declared
payload field, which is the field the redaction machinery can reach.
The actor column is a principal id, and the host resolves the actor a module
sends against the module's authenticated identity: a principal id in any
spelling is stored canonical; the module's own principal is recorded as
`actor_type=system` and any other principal — a subject the module acted for —
as `actor_type=agent`, but only once that subject is a member of the tenant the
row lands in, so a module bound to one tenant cannot write another tenant's user
id into its trail. Note what that check does and does not assert: the host sees
no evidence of who acted and the module is the only witness, so a module-named
subject is the **module's claim**, not a verified initiator in the sense
`AuditActor` carries elsewhere. A subject the tenant does not contain is refused
the same way an unresolvable actor is, and a membership read that fails surfaces
as `Internal` rather than being read as "not a member" — it proves nothing either
way, and a row written under a guessed actor can never be put right. A process label of the module's own, `system:<process>`
(lowercase), is the module's own work and is recorded as the module's principal
with `actor_type=system`. Any other actor names no principal and is refused with
`FailedPrecondition` (`module audit actor names no principal`), never stored
with the actor blanked; the event is well formed, so a producer keeps it pending
rather than settling it as invalid. The directory the audit surfaces name actors
from (`ListPrincipals`) lists each module that may act in the organization under
its prefix, on the first page — the page every directory walk reads, and taken
from that page's own budget so the page still honours its `page_size` — so a
module's rows read as that module rather than as an unknown actor. Such an entry
has no stored row: it carries no `created_at`, and `GetPrincipal` and
`RevokePrincipal` answer `NotFound` for it, which is the fail-closed answer.
Its authority comes from the `MODULE_PRINCIPALS` declaration, so that
declaration is what removes it.
Therefore source completion/failure producers must send
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

`saas.document.dead_letter_redriven` records an operator re-queuing a
document's dead-lettered derivation, one event per re-queued producer run. The
entry is entry_id and the emission is refused without one. In addition to
document provenance it requires:

| Field | Meaning |
| --- | --- |
| `version` | The document version the re-queued run derives from |
| `correlation_id` | The operator's whole redrive, repeated on every entry it re-queued |
| `producer` | The stage that had given up, at most 128 bytes |
| `error_class` | `permanent`, `exhausted`, `cancelled`, or `unknown` |

`permanent` means a stage called the input unreadable and `exhausted` that the
retry budget was spent. `cancelled` is a run abandoned with no verdict — dropped
in flight, or dead-lettered by hand — and `unknown` a dead letter that carries no
classification. Record what the dead letter says; a run that fits neither of the
first two is `cancelled` or `unknown`, never filed as unreadable input. All four
are required nonempty: a blank is refused, not stored as an unnamed stage.

The failure text itself is never on the spine. Unknown payload fields are
rejected and `producer` is bounded, so there is no field to put it in.

The emission idempotency key is required and is correlation ID, entry and
producer. Keyed on the operation rather than on the entry alone, a transport
retry of one re-queue collapses to a single row while a genuinely second redrive
of the same entry still records. An empty key deduplicates nothing, so the
emission is refused rather than double-counting a replayed response; the host
cannot supply the key itself, because only the emitter knows whether two emits
are one operation retried or two real operations.
Count distinct `correlation_id` for operator redrive operations and rows for
re-queued runs; a redrive interrupted part-way leaves the rows it completed, so
the two counts disagreeing means an incomplete operation, not a small one.

A dry run, and a run the redrive left alone, record nothing.

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
