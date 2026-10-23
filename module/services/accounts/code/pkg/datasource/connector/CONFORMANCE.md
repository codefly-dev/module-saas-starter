# Connector conformance: where each provider stands

The datasource connector model ships with an envelope (`envelope.go`), a
descriptor-driven registry (`registry.go`) and a conformance suite
(`connectortest`). **Two connectors pass it: GitHub and object storage.** The
generic API and the crawler run on engines of their own, are registered
non-conformant with a gap, keep their existing sources running, and take no new
ones.

A suite with one conformant implementation had not shown the model generalises,
which was the reason for having one; object storage is the second, on a store
that answers "what is there now" rather than "what changed since". This document
is the honest record: per connector, what it honours, what remains, and what
closing it would actually take. It is held to `pkg/business/datasource_connectors.go`
by `connector_conformance_record_test.go`, so a provider cannot be registered
here without a section, or given a section that names no registered provider.

## Reading the clauses

The envelope's clauses are listed at the top of `envelope.go`. Two of them are
not a connector's to fail and are omitted from every table below: clause 7
(audit) is the host's — it emits one vocabulary for every connector — and clause
3 (credentials) is honoured by all four, because the host decrypts and hands the
credential in per call and no connector persists or returns one.

One structural fact applies to three of the four, ahead of any clause:
`Registry.Register` admits only the **files** interface, because it is the only
interface with a bulk fetch in this host. A `records` or `pages` connector
therefore cannot be conformant however well it behaves — the host has nowhere to
call it from. Closing that is a host change, not a connector change, and it
gates the API and crawler connectors below regardless of their own gaps.

## github — conformant

Passes every case in `connectortest.RunFiles`
(`pkg/datasource/github/connector_conformance_test.go`): identity and tenancy,
readers (source-scoped, so they fail closed to the boundary's administrators),
the credential never leaving the connector, exact incremental changes with a
resync on rewritten history, deletions, provenance, bulk fetch in a bounded
number of provider calls, typed rate limits with a reset time, typed refusals
for an over-limit item or batch, and cancellation.

## upload — object storage, conformant

**Conformant.** `objectstore.FilesConnector` passes `connectortest.RunFiles`
through `objectstore.ServiceBucket` — the host's client of the composed
object-storage service (codefly-dev/service-object-storage, its per-call bucket
binding and batched read from #108) — over an in-process fake of the service's
gRPC API, so every clause is measured on the calls the host makes
(`objectstore/connector_conformance_test.go`). The host signs, dials and guards
nothing for a bucket; `client.go`, its own store client, is deleted. It is
registered through `connector.Budgeted`, metered per provider credential like
GitHub, and `AdmitNewSource` admits it; the host admits a new source only where
a composed object-storage service can read a bucket (below).

| Clause | How it is honoured |
| --- | --- |
| 1 identity and tenancy | `connector.KeyFor(source, object key)`: the object key is the item id and its ETag its version. |
| 2 readers | `ReadersSourceScoped`: a bucket has no per-object access list, so every object is readable by whoever may read the source's collection. |
| 4 versions and change sets | A version is a digest of the listing within the source's key filter and object cap, under its binding; the host retains the listing each version names in `datasource_object_manifests` (migration 46; the newest four per source), and `Changes(from)` diffs against it. The host's own sync still takes a complete snapshot each time and re-sends every object, closing with the listing that tombstones the rest; delivering only what changed means moving `runUploadSync` onto the change-set path, which changes what the consuming module receives, so it is its own change. |
| 5 provenance | Source, org, boundary, version, key and ETag on every fetched file. |
| 6 budget and backpressure | A declared `Budget`, metered by `datasource_credential_budgets` through `connector.Budgeted`, keyed by the credential — a keyed digest of the store's origin and the access key id, fixed at connect — so every source reading with one key at one store spends one quota and honours one provider throttle. A sync a person asks for may spend the reserved share; the scheduled pass yields. |
| 8 bulk only | A batch is one `GetMany` per `max_get_many_items` (1000, the connector's batch): 120 objects cost one call after the listing. |

**How it reads a store.**

- **The binding.** `objectstore.ServiceBucket` names the source's endpoint,
  region and bucket and carries its access key for each call; the service
  never stores it. The binding carries **no prefix**: the service normalizes a
  binding's prefix as a folder (no leading or trailing `/`), while a source's
  prefix is a literal S3 key prefix — `/reports/` is not `reports/`, and `/` is
  not the whole bucket. Every listing filters on the literal prefix, which the
  service passes to the store unchanged, and every key travels whole, which is
  what the consuming module has always been sent. A listed key outside the
  literal prefix, or a page past the service's own page size, refuses the whole
  listing. Before it lists or reads, it asks `Capabilities` under the binding
  and refuses a service that reports no bound caps or no batch size — an older
  one would serve its own bucket as the tenant's.
- **Every call has a deadline**: 30 seconds per listing page or capability
  question, five minutes per batched read. A store that answers and then
  stalls does not hold a sync, and its worker's lease, past it.
- **Admission needs a service that can read a bucket.** At start the host asks
  the composed service, unbound, whether it reads a batch; one that does not —
  the released 0.0.18 agent, or none composed — leaves object storage
  unavailable: the catalog reports `accepts_new_sources: false` and `AddSource`
  refuses by name.
- **The guard and the caps are the service's.** A non-public endpoint is
  refused there by name (`PERMISSION_DENIED`, reported as the envelope's
  credential refusal: it is the source's configuration to fix either way). A
  bound read is capped at 5 MiB, the connector's `MaxObjectBytes`, so an object
  over it is refused from its listed size before it is read; one the service
  still answers `TOO_LARGE` is the envelope's `ErrItemTooLarge`. A listing is
  paged at the service's page size and bounded at 20 pages; past that it is
  `ErrListingIncomplete`, never a partial version. **Completeness is the
  service's to report:** the host takes an empty `next_page_token` as the end
  of the listing, so a store that answers "truncated" with no continuation
  token must be refused by the service, which owns the S3 parsing — the host
  cannot recover a truncation signal the service discards.
- **The scope is the connector's.** The key filter and the object cap are the
  source's `objectstore.Scope`, applied to the listing before its digest: an
  excluded object is never read and never counts against the cap, and a
  listing the cap cuts is `ErrListingIncomplete`, which fails the sync
  terminally with nothing delivered.
- **A fetch is at a version.** Each object of a batch is read at `If-Match` its
  listed ETag; one rewritten or deleted since (`CHANGED`, `NOT_FOUND`) is
  `ErrItemNotFound`, which fails the sync without a listing, and the next sync
  lists afresh.

## crawler — documentation website

**Registered gap:** no cursor and no deletions (a page leaving the sitemap is
never removed), and one fetch per page.

| Clause | Where it stands |
| --- | --- |
| 1 identity and tenancy | Partial. The page URL is a stable id; the enqueued job names the source, org and boundary. Nothing carries a provider item version — `lastmod` in the sitemap is read but not retained. |
| 2 readers | Source-scoped, as above: a public documentation site has no per-page access list. Nothing to close. |
| 4 versions and change sets | **Gap, as registered.** No cursor, and no closing listing, so a page that leaves the sitemap keeps its entry in the collection for good. Unlike object storage, the crawler never sends the inventory that would let the store tombstone it. |
| 5 provenance | The URL rides on each delivery; no version. |
| 6 budget and backpressure | No declared `Budget`; unmetered. |
| 8 bulk only | One HTTP GET per page. |
| interface | `pages`, which the registry cannot admit at all (see above). |

**What closing it takes.** The deletion half is the cheaper one and is the same
mechanism object storage already uses: close a crawl that read the whole sitemap
with the complete listing of the pages it delivered. The cursor half wants
`lastmod` retained per page so an unchanged page is not re-fetched. Both are
ahead of the interface problem, which has to be solved first for the connector
to be registrable as conformant at all.

## api — HTTP API

**Registered gap:** one untyped response with no item identity, no cursor and no
deletions; to be rebuilt as a records connector or retired.

| Clause | Where it stands |
| --- | --- |
| 1 identity and tenancy | **Not met, structurally.** The source names one resource path and the sync enqueues its whole response body as a single delivery. There are no items, so there is nothing for an item id to identify. |
| 2 readers | Not applicable until there are items. |
| 4 versions and change sets | **Not met.** No version, no cursor, no deletions. Re-syncing unchanged content dedupes on the content hash, which is not the same thing: it avoids a duplicate delivery, it does not describe a change. |
| 5 provenance | Only the source and a content hash. |
| 6 budget and backpressure | Calls and legacy sync share the host Scheduler: 60 requests per minute per credential key, with 80% available to background work. Provider 429 responses block that key until reset; the call surface carries typed retry metadata. OAuth refresh is row-locked and permanent rejection requires reconnecting. |
| 8 bulk only | One request per sync — trivially satisfied, and for the wrong reason: there is only ever one thing to fetch. |
| interface | `records`, which the registry cannot admit (see above). |

**What closing it takes.** More than closing a gap: deciding what an item *is*
for a generic HTTP API. The response would have to be projected into records
under a caller-declared identity (a JSON pointer to the id, one to the version),
which is a new configuration surface and a new contract, not a repair of this
one. The registered gap says "rebuilt as a records connector or retired", and
that is still the honest position — this connector is not one clause away from
conformance, it is a different design.

### Operations admission

The API call path is separate from this sync envelope. Its descriptor retains
`accepts_new_sources: false` for sync and reports `accepts_operations` separately.
Connecting an API for operations creates no sync job or reconcile schedule;
legacy API sources continue syncing. A call declaration is an explicit bounded
HTTP route and JSON contract, not a record change-set interface.

`operations/declaration_test.go` covers declaration digests, schema refusal,
required path parameters, escaping, query/body routing, and GET body refusal.
`apisource/operations_test.go` covers methods, byte caps, typed provider rate
limits, token endpoint guards, and transport disclosure. `apisource/guard_test.go`
holds the egress guard to the shared `auth/egresstest` vectors, one named case
per non-global range, and proves the operation dial and every token exchange
run the guard on the address being dialed; `crawler/guard_test.go` does the
same for the crawler. Object storage dials nothing: its guard is the
object-storage service's.
`connectortest.RunOperations`, run by `business/TestAPIOperationsConformance`,
checks the host call path for unknown operations, schema and path refusals,
GET body refusal, output caps, typed 429 responses and a lost mutation reply
that cannot be dispatched again. Its scripted provider uses the transport
substitution seam; it does not qualify the public Connect binding or real
Postgres receipts. `adapters/TestSourceOperationPreparedConnectJSONReplayAndCurrentAuthority`
derives and prepares the binding, then calls the actual replay handler in Connect
JSON mode with an in-memory receipt store. It checks input conflicts and current
permission, personal owner and declaration revocations. The business integration
suite's `TestSourceOperationRealReceiptReplayAndUnknownMutation` uses real
Postgres, Vault and the SDK transaction writer. Passing the in-memory test does
not qualify database commit/rollback or cross-process serialization.

## Summary

| Connector | Conformant | Blocked by the interface | Deletions | Cursor | Budget |
| --- | --- | --- | --- | --- | --- |
| github | yes | — | yes | yes | declared and metered |
| upload | yes | — | yes | yes (the host's sync still re-sends every object) | declared and metered |
| crawler | no | yes (pages) | no | no | no |
| api | no | yes (records) | no | no | no |

Object storage is the second conformant implementation, which is what shows the
model generalises: the same envelope over a store with no history of its own,
where the cursor is a listing the host retains.
