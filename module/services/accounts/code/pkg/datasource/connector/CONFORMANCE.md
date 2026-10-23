# Connector conformance: where each provider stands

The datasource connector model ships with an envelope (`envelope.go`), a
descriptor-driven registry (`registry.go`) and a conformance suite
(`connectortest`). **GitHub is the only connector that passes it.** The generic
API and the crawler run on engines of their own; object storage syncs through
its envelope connector but not yet within the suite's bulk clause. All three
are registered non-conformant with a gap, keep their existing sources running,
and take no new ones.

A suite with one conformant implementation has not yet shown the model
generalises, which was the reason for having one. This document is the honest
version of that: per connector, what it already honours, what remains, and what
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

## upload — object storage

**Registered gap:** every sync re-sends every object in scope, and the
object-storage service reads one object per call; deletions ARE applied,
through the complete listing that closes a whole-scope sync.

`runUploadSync` reads through `objectstore.FilesConnector`, and the connector
reads through the composed object-storage service
(codefly-dev/service-object-storage, its per-call bucket binding from #108):
the host signs, dials and guards nothing for a bucket. `client.go`, the host's
own store client, is deleted.

| Clause | Where it stands |
| --- | --- |
| 1 identity and tenancy | Honoured, on the envelope: `connector.KeyFor(source, object key)`, the object key as the item id and its ETag as its version. |
| 2 readers | `ReadersSourceScoped`: a bucket has no per-object access list, so every object is readable by whoever may read the source's collection. Nothing to close. |
| 4 versions and change sets | Honoured by the connector: a version is a digest of the listing within the source's key filter and object cap, under its binding; the host retains the listing each version names in `datasource_object_manifests` (migration 46; the newest four per source), and `Changes(from)` diffs against it. **The host's sync does not use it yet:** it takes a complete snapshot each time and re-sends every object, closing with the listing that tombstones the rest. Delivering only what changed is what moving `runUploadSync` onto the change-set path would buy; it changes what the consuming module receives, so it is its own change. |
| 5 provenance | Source, org, boundary, version, key and ETag on every fetched file. |
| 6 budget and backpressure | A `Budget` is declared; the provider is not yet registered through `connector.Budgeted`, so an object-storage sync is not metered by `datasource_credential_budgets`. |
| 8 bulk only | **The registered gap.** `FetchFiles` asks the `Bucket` for a batch in one `Read`, and the conformance suite's scripted bucket serves it in one call; the service's bound `Get` takes one key, so a batch of N objects is N calls (`objectstore/service_test.go` pins that). A batched bound read is service-object-storage#108's. |

**How it reads a store.**

- **The binding.** `objectstore.ServiceBucket` names the source's endpoint,
  region, bucket and prefix and carries its access key for each call; the
  service never stores it. A source's prefix is a key prefix and a binding's is
  a folder, so the binding takes the prefix up to its last `/` and the listing
  filters on the rest; an item keeps its full object key, which is what the
  consuming module has always been sent.
- **The guard and the caps are the service's.** A non-public endpoint is
  refused there by name (`PERMISSION_DENIED`, reported as the envelope's
  credential refusal: it is the source's configuration to fix either way). A
  bound read is capped at 5 MiB, the connector's `MaxObjectBytes`, so an object
  over it is refused from its listed size before it is read. A listing is paged
  at the service's page size and bounded at 20 pages; past that it is
  `ErrListingIncomplete`, never a partial version.
- **The scope is the connector's.** The key filter and the object cap are the
  source's `objectstore.Scope`, applied to the listing before its digest: an
  excluded object is never read and never counts against the cap, and a
  listing the cap cuts is `ErrListingIncomplete`, which fails the sync
  terminally with nothing delivered.
- **A fetch is at a version.** `Read` asks for each object at `If-Match` its
  listed ETag; an object rewritten or deleted since is `ErrItemNotFound`, which
  fails the sync without a listing, and the next sync lists afresh.

What remains before the provider is registered conformant: a batched bound read
in the service (clause 8), and the connector registered through
`connector.Budgeted` (clause 6). The gap sentence above is deleted then.

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
| upload | no | no (files) | **yes** | built, not yet used by the sync | declared, not metered |
| crawler | no | yes (pages) | no | no | no |
| api | no | yes (records) | no | no | no |

Object storage is the nearest: it is on an admissible interface, applies
deletions, and syncs through its envelope connector over the object-storage
service, whose cursor passes the suite against a scripted store. What stands
between it and a second conformant implementation is a batched read in the
service and the budget registration. That is what would demonstrate the model
generalises.
