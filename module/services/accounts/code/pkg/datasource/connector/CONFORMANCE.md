# Connector conformance: where each provider stands

The datasource connector model ships with an envelope (`envelope.go`), a
descriptor-driven registry (`registry.go`) and a conformance suite
(`connectortest`). **GitHub is the only connector that passes it.** The generic
API, the crawler and object storage run on engines of their own, are registered
non-conformant with a gap, keep their existing sources running, and take no new
ones.

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

**Registered gap:** no cursor, so every sync re-sends every object, and one
fetch per object; deletions ARE applied, through the complete listing that
closes a whole-folder sync.

| Clause | Where it stands |
| --- | --- |
| 1 identity and tenancy | Honoured in substance. The object key is the provider's stable id and the ETag is its version; every enqueued job names the source, org and boundary. Not expressed through `connector.Key`/`Item`, because the connector is not on the envelope. |
| 2 readers | No per-object access list is read, so every object is readable by whoever may read the boundary — which is `ReadersSourceScoped`, the model's own answer for a provider like this. Nothing to close. |
| 4 versions and change sets | **The remaining gap.** There is no version to diff from, so `runUploadSync` re-lists and re-fetches the whole prefix on every sync. Deletions are not missing: a sync that saw the whole folder and delivered every object in it closes with the complete listing of what it delivered, and the store tombstones the rest; a failed or truncated listing sends none, so a partial view can never delete. |
| 5 provenance | Key and ETag ride on each delivery; not in the envelope's shape. |
| 6 budget and backpressure | No declared `Budget`, and the connector is not metered by `datasource_credential_budgets`, so an object-storage sync cannot yield to a person's sync the way a GitHub one does. |
| 8 bulk only | One `GetObject` per object. The listing is one call; the fetches are not. |

**What closing it takes.** A `FilesConnector` over S3 whose version is a digest
of the listing (key → ETag) and whose `Changes(from)` diffs the current listing
against the one `from` names — which needs the previous listing to be retained,
since S3 has no "what changed since" API. That is a store-shaped change, not
just a connector: the host would have to keep the last manifest per source. Not
done here because it is a design question about where that manifest lives, not a
gap in a flow that already exists.

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
limits, token endpoint guards, and transport disclosure.
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
| upload | no | no (files) | **yes** | no | no |
| crawler | no | yes (pages) | no | no | no |
| api | no | yes (records) | no | no | no |

Object storage is the nearest: it is already on an admissible interface and
already applies deletions, and one cursor stands between it and a second
conformant implementation. That is what would demonstrate the model generalises,
and it is the one worth doing next.
