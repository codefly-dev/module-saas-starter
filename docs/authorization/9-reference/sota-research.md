# SOTA research — landscape & sources

> **Input, not decision.** State-of-the-art survey (2024–2026) informing the
> proposals. Verdicts here are research recommendations; the *decisions* live in
> the RFCs and ADRs. Full narrative + diagrams are in the published reference
> artifact; this is the durable, linkable source list.

## Fine-grained / ReBAC (gaps 1, 2, 5) → RFC-0001, RFC-0002
Zanzibar-style relationship tuples are the industry answer to hierarchy +
per-record sharing + subtree. **Recommendation:** build Postgres-native first
(single source of truth, atomic with RLS); adopt a PDP only at the inflection
where nested groups outgrow SQL. RLS stays the floor regardless.
- [Zanzibar paper](https://www.usenix.org/system/files/atc19-pang.pdf) ·
  [OpenFGA parent-child](https://openfga.dev/docs/modeling/parent-child) ·
  [SpiceDB caveats](https://authzed.com/docs/spicedb/concepts/caveats) ·
  [WorkOS FGA](https://workos.com/docs/fga) ·
  [consistency/zookies](https://authzed.com/docs/spicedb/concepts/consistency)

## Hierarchical scope in Postgres → RFC-0001
`ltree` materialized paths: GiST-indexed ancestor test (`@>`), most-specific-wins
via `nlevel() DESC`. Preferred over adjacency/closure for a read-heavy, auth-
critical tree.
- [ltree docs](https://www.postgresql.org/docs/current/ltree.html) ·
  [hierarchical models in PG](https://www.ackee.agency/blog/hierarchical-models-in-postgresql) ·
  [ltree vs closure](https://dev.to/dowerdev/implementing-hierarchical-data-structures-in-postgresql-ltree-vs-adjacency-list-vs-closure-table-2jpb)

## Capability tokens / delegation (chain authorship) → RFC-0003
The Work Context chain is a signed attenuating capability token (Macaroon/Biscuit/
UCAN family) and already does attenuation well. **Borrow, don't replace:**
content-address for durability (UCAN), per-hop revocation IDs (Biscuit), third-
party blocks for cross-service, RFC 8693 `act` for interop.
- [Macaroons paper](https://research.google/pubs/pub41892/) ·
  [Biscuit revocation](https://www.biscuitsec.org/docs/guides/revocation/) ·
  [UCAN revocation](https://ucan.xyz/revocation/) ·
  [RFC 8693 (act claim)](https://www.rfc-editor.org/rfc/rfc8693.html) ·
  [agent provenance 2026](https://blog.identity.foundation/building-ai-trust-at-scale-4/)

## Policy engines / ABAC (gap 4)
Keep policy in tested Go while the conditional set is small (closed enum of
predicates; optional hand-written predicate→SQL over RLS). `cedar-go` (GA,
formally verified) only past a threshold. Reject OPA/Oso-Cloud as a decision
service.
- [cedar-go 1.0](https://www.strongdm.com/blog/strongdm-cedar-go-1-0-0-policy-authorization-go-developers) ·
  [Cedar Analysis](https://aws.amazon.com/blogs/opensource/introducing-cedar-analysis-open-source-tools-for-verifying-authorization-policies/) ·
  [OPA partial-eval → SQL](https://www.openpolicyagent.org/docs/filtering/partial-evaluation) ·
  [Casbin](https://casbin.apache.org/)

## Field-level (gap 3)
Minimal redaction interceptor reusing `CheckPermission`; masking/encryption for
regulated PII only. Mostly out of scope.
- [protoc-gen-redact](https://github.com/Shivam010/protoc-gen-redact) ·
  [PG Anonymizer masking](https://postgresql-anonymizer.readthedocs.io/en/latest/dynamic_masking/) ·
  [CipherStash](https://cipherstash.com/stack/encryption)

## Workload identity (service-to-service)
Don't adopt SPIFFE yet; rotate the internal token + per-service `kid`, add a
nestable `act` claim to the minted JWT. SPIFFE later, under the JWT, at scale.
- [SPIFFE vs client-creds](https://mojoauth.com/blog/workload-identity-for-agents-spiffe-spire-vs-oauth-client-credentials) ·
  [phantom token](https://curity.io/resources/learn/phantom-token-pattern/) ·
  [agent identity two-plane](https://next.redhat.com/2026/06/10/wiring-zero-trust-identity-for-ai-agents-spiffe-token-exchange-and-kagenti/)

## Production references (hierarchy + sharing)
Drive/GitHub/Notion/Linear converge on: default-deny metadata filter → inherited
container permission → per-record ACL overlay; highest-permission-wins.
- [Zanzibar/Drive ReBAC](https://www.aserto.com/blog/google-zanzibar-drive-rebac-authorization-model) ·
  [authorize like GitHub](https://www.aserto.com/blog/authorize-like-github) ·
  [Slack/Notion/Linear permissions](https://workos.com/blog/multi-tenant-permissions-slack-notion-linear)
