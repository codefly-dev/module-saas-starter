# ADR 0006: Audit sink and retention tiers — a swappable compliance backend, Postgres as the atomic hot source of truth

- Status: Proposed
- Date: 2026-09-02
- Task: prepares the audit performance/size and compliance workstreams (see
  #430). Refines — for the **sink and retention dimension only** — the
  "keep audit in Postgres" stance of [ADR 0003](./0003-typed-audit-event-registry.md).
  It does not change code.

## Context

Audit is not like the rest of the SaaS data. Two properties make it a special
case that the current single-backend design does not serve:

1. **Growth.** Audit is append-only and emitted on every protected mutation (and,
   increasingly, on sensitive reads). In a multi-tenant platform it grows faster
   than any other table — it is the first store to dominate Postgres size and to
   make vacuum, index maintenance, and backups expensive. ADR 0003 accepts the
   single-table STI model *"with time partitioning to bound the last con"* (one
   hot table at very high volume); that partitioning is **not yet built**.

2. **Compliance.** Audit is a legal/compliance artifact, not just telemetry.
   Different records need different **retention windows**, **immutability / WORM**
   guarantees, **data residency**, and **legal-hold** handling — often longer and
   stricter than operational data, and sometimes governed by regimes that mandate
   an off-database, tamper-evident archive. PLATFORM_REFERENCE already names a
   *"7-year content-free / 90-day full"* two-tier split, but it **is not a
   distinct construct** today. Retention is worse than absent: `RunRetention`
   collides with the immutability trigger and **deletes nothing** (ADR 0003, gap
   analysis).

Today there is exactly one hardcoded backend (`NewDurableAuditEmitter`, Postgres,
wired in `services/accounts/code/work.go`), the only external sink that ever
existed (a per-org customer S3 export) was removed, and there is no configuration
switch. ADR 0003 deliberately kept analytics in Postgres and called an external
OLAP store *"a documented escape hatch, not the plan."* This ADR refines that
stance **for the audit sink and retention dimension only** — analytics *queries*
stay SQL-over-Postgres and ADR 0003 is otherwise unchanged.

## Product audit vs. compliance audit — two requirement sets

The same event stream serves two audiences with different needs; conflating them
is what would push everything into one expensive store. Separating them is what
lets most deployments stay on Postgres.

| Dimension | **Product audit** (operational / UX) | **Compliance audit** (legal / assurance) |
| --- | --- | --- |
| Purpose | Activity feed, per-user/tenant history, "who changed this?", recent security events | Legal defensibility, external audit, incident forensics, regulatory retention |
| Consumers | End users, tenant admins, support, in-app UI | Auditors, DPO / legal, security investigations |
| Query pattern | Interactive, frequent, filtered, joined to live resources | Rare, bulk export, point-in-time |
| Latency / freshness | Low latency, near-real-time | Tolerant; eventual is fine |
| Fields | Rich, human-readable, joinable | May be content-free (hash + envelope) |
| Retention | Short — 90 days to ~1 year | Long — years (e.g. 7) |
| Integrity | RLS + append-only | WORM / tamper-evident, legal hold |
| Residency | Follows the app database | May be region-pinned by regime |
| Natural home | **Postgres (hot, partitioned)** | **Archive / swappable sink (cold)** |

The **product** requirement is already well served by Postgres (fast, RLS-scoped,
joinable, the typed registry). The **compliance** requirement — long retention,
WORM, residency, legal hold — is the part that strains Postgres and is largely
unbuilt. This is why the design below is **Postgres-first**, not warehouse-first.

## Decision

Treat audit as a **tiered stream**, Postgres-first, with an **opt-in** swappable
compliance sink; keep Postgres as the atomic hot source of truth.

1. **Postgres stays the source of truth on the write path.** The durable
   transactional outbox — the audit row and the webhook fan-out committed inside
   the caller's transaction — is preserved. No external system goes on the
   synchronous mutation path: the fail-closed *"no state change without its
   compliance record"* guarantee is non-negotiable, and an external system cannot
   join a Postgres transaction.

2. **Postgres + partition offload is the default; the external sink is opt-in.**
   Most deployments need only the Postgres hot tier plus **partition offload to
   cheap storage** (item 3) to satisfy retention — no external service required.
   A pluggable `AuditSink` is enabled **only when a compliance regime demands**
   off-database WORM / immutability, data residency, or legal hold that Postgres
   cannot provide. When enabled it receives events from the durable outbox
   **asynchronously** (a tee, not a swap; at-least-once, ordered per org),
   selected by configuration (`AUDIT_SINK = postgres | archive | both`,
   extensible). Backends: a WORM / immutable object or ledger archive; a
   region-scoped store; or an external compliance system. Because it is off the
   transaction path, a slow or unavailable sink never blocks or fails a business
   write.

3. **Tiered retention as an explicit construct.** Records move through tiers with
   independent policies:
   - **Hot (Postgres):** full-fidelity, short window, **time-partitioned**;
     serves operational query/UI and the typed registry (ADR 0003). Cold
     partitions are **detached and offloaded**, not row-deleted — which also
     fixes the retention-vs-immutability collision.
   - **Cold / archive (swappable sink):** long / compliance window; **full or
     content-free** (hash + envelope) per policy; immutability / WORM and
     residency enforced at this tier; **legal hold** overrides deletion.
   Per-tenant and per-event-type retention and residency are **policy inputs**
   (an extension of `data_retention_policies`), not code.

4. **Two workstreams, distinct concerns:**
   - **Performance / size:** time-partition `audit_events`, detach + offload cold
     partitions, and repair `RunRetention` to operate by partition detach rather
     than row delete.
   - **Compliance:** the swappable sink plus the retention / immutability /
     residency / legal-hold tiers.

## Options considered

| Option | Sketch | Verdict |
| --- | --- | --- |
| **A. Status quo** — Postgres only, row-delete retention | One hardcoded backend; `RunRetention` deletes old rows | **Rejected** — retention is a no-op today; Postgres grows unbounded; no off-database, tamper-evident compliance archive |
| **B. Swap backend** — replace Postgres with an external store | Point the emitter at an external system | **Rejected** — breaks the atomic-with-mutation outbox and the webhook fan-out; an external system cannot join the Postgres transaction |
| **C. Tee to a swappable sink + tiered retention, Postgres source of truth** *(proposed)* | Async fan-out from the durable outbox to a config-selected sink; hot Postgres tier + cold archive tier | **Accepted** — keeps the write-path guarantee; enables off-database long-term/WORM retention and residency; bounds Postgres growth |
| **D. Reinstate per-org customer S3 export only** | Bring back the removed per-org JSONL export | **Rejected as the endpoint** — one narrow, per-customer backend, not a platform retention/compliance construct; folded into C as one selectable sink |

## Consequences

- **Postgres-first: most deployments never stand up an external sink.** Hot
  Postgres (product tier) + partition offload + a retention policy is the default
  and is enough whenever the compliance horizon fits cheap offload; the swappable
  sink is the escape valve for regimes that mandate off-database WORM, residency,
  or legal hold. The sizing below is what tells a given deployment which it is.
- Enables off-database, tamper-evident, long-horizon retention without bloating
  the operational database, and gives residency/WORM/legal-hold a real home.
- Preserves the strong synchronous write-path guarantee (fail-closed, atomic with
  the mutation, webhook fan-out intact).
- New moving parts: async delivery (at-least-once, per-org ordering, backpressure),
  a partition lifecycle, and per-tier policy evaluation.
- Refines ADR 0003's "keep it in Postgres" **only** for the sink/retention
  dimension; the analytics-query stance (SQL over the same Postgres, no warehouse
  lock-in) is unchanged.
- The removed per-org S3 export is superseded as *the* mechanism; if reintroduced,
  it is one selectable backend behind `AuditSink`.

## Admission policy — what may be recorded as an audit event

Audit is expensive to keep (see sizing) and legally load-bearing, so it must not
be abused as a general logging channel. The audit log records **accountability
facts: who did what, to which resource, in which tenant, when** — not diagnostics
and not telemetry. Enforcement is the **typed registry** (ADR 0003): only
**registered** event types may be written; a new type needs a catalog entry and
review, and its payload is validated against the registered schema. There are no
free-form `action` strings.

**Admit as an audit event only if all hold:**

- It is a **decision or state change with accountability value** — a privileged,
  mutating, consented, or security-relevant action: authorization grant/deny at a
  policy boundary, role/permission/entitlement change, impersonation, login /
  MFA / session lifecycle, data export or deletion, tenant/config/billing change,
  delegation mint / exchange / revoke, platform-admin actions.
- It names a **subject actor**, a **resource**, a **tenant/org** (or is explicitly
  a system event), and a **time**.
- It is something you would want to produce in a **compliance review or incident
  investigation**.

**Do NOT put in audit — route elsewhere:**

- Debug / diagnostic logging, stack traces, request/response bodies → application
  logs and OTEL traces.
- High-frequency telemetry, counters, latencies, health → metrics (the telemetry
  service).
- Product behavior / funnel / engagement → product analytics (canonical product
  events). Per PRODUCT_ANALYTICS, **product events must not be inferred from audit
  rows**, and the reverse holds too.
- Read-path chatter and list endpoints at volume → **not** one audit row per read.
  Where access logging is genuinely required, use the *audit-the-access-set*
  pattern (record the set of IDs, capped, with a dedupe window) — see the
  "11k-rows-from-a-poller" cautionary tale in PLATFORM_REFERENCE — never a row per
  item.
- Secrets, tokens, or PII beyond the redaction policy → redact per policy; never
  store raw payloads.

**Anti-abuse guardrails:** registry-gated types (unregistered → rejected);
per-type schema validation with bounded fields (no dumping request bodies); a
size/rate budget per event type; read events use capped access-set aggregation,
not per-item rows.

## Sizing — back-of-the-envelope (why moving out of Postgres is warranted)

Order-of-magnitude only, to size the decision — not a benchmark. Assume an
effective **~1 KB per stored row** (typed row + JSONB payload + indexes;
index overhead alone is often 1–3× the base row, so treat 1 KB as conservative
and 2 KB as a fat-payload case). One audit event/second ≈ **31.5M rows/year**.

| Sustained write rate | Rows / year | ~GB / year @1 KB | ~GB / year @2 KB |
| --- | --- | --- | --- |
| 10 events/s (small) | ~315 M | ~315 GB | ~630 GB |
| 100 events/s (medium) | ~3.15 B | ~3.1 TB | ~6.3 TB |
| 1 000 events/s (large) | ~31.5 B | ~31 TB | ~63 TB |

Now apply a **7-year compliance horizon**: multiply annual by 7 → small ≈ **2 TB**,
medium ≈ **22 TB**, large ≈ **220 TB**. Even a small deployment's audit stream
alone outgrows a typical operational database; medium-and-up is plainly
infeasible to keep in the hot Postgres instance.

Contrast the **hot window**: a 90-day full-fidelity tier is ~90/365 ≈ **0.25×**
one year — medium ≈ **~0.8 TB** hot (time-partitioned, prunable), with the 7-year
tail (~22 TB) living in the cheap/WORM archive. That split is exactly the tiering
in this ADR.

This also quantifies the admission policy's value: abusing audit for, say, an
extra 50 events/s of debug logging would add **~1.5 TB/year** (~11 TB over the
compliance horizon) of tamper-proof, must-retain storage — a direct, expensive
reason to keep non-audit traffic out of the audit log.

## References

- [ADR 0003](./0003-typed-audit-event-registry.md) — typed audit registry;
  immutability / RLS / retention baseline and its gap analysis (retention no-op).
- #430 — configurable audit sink (async tee); #170 — audit review and design.
- PLATFORM_REFERENCE — §1.8 audit emission; the Retention/redaction and
  Object-storage rows.
