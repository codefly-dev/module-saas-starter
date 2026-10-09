
# Installed delegated audience exchange

`ModuleCapabilitiesService.ExchangeDelegatedReadAudience` is an internal,
read-only exchange for a module holding a current signed parent but no owner
bearer. The module authenticates independently with its own module Work Context
and the internal perimeter credential. Its request contains only `binding_id`
and `parent_work_context_token`. No owner, actor, target audience or scope is
caller selectable. The parent must address the authenticated module's installed
prefix and tenant; the canonical current-revision and every-hop revocation
checks run before exchange and again before returning the child. Delegated
parents require a configured durable journal. Actor ceilings and scope
attenuation use the same signer and authority path as owner audience exchange.

Installation declares an optional `read_audiences` map in the existing
`module-capabilities/MODULE_PRINCIPALS` registry, for example:

```json
{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","read_audiences":{"proof":{"audience":"example-producer","scopes":[{"resource_kind":"results"}]}}}}
```

Each installed scope may additionally restrict `resource_ids`. Actions are
always exactly `read`. The incoming parent must already admit all installed
scopes; installation never confers new viewer authority. A child lives at most
60 seconds and less than the remaining parent lifetime. Owner, tenant, Task,
Session and actor/delegation chain remain unchanged. The response is secret and
must not be logged. No persisted credential or broad signing port is created.
Interactive calls can continue using the existing owner-bearer
`WorkContextService.ExchangeAudience` API.

`ModuleCapabilitiesService.ExchangeDelegatedOperationAudience` applies the same
identity, tenant, current-authority, journal, recheck, lifetime and secrecy
rules to an installed operation binding. Its request contains the authority it
presents, `binding_id`, and a `lookup` selector only. The caller cannot supply
the target audience, scopes, actions, resource ids or TTL.

The authority is exactly one of two things, and which one it is decides how long
the work behind it may run.

- `parent_work_context_token` is a live capability the caller holds. The child
  is attenuated against it and expires with it, so nothing admitted this way can
  outlive the parent — in practice 60 seconds, and never more than the
  15-minute ceiling every Work Context has.
- `delegation_id` is a **reference** to a host-owned, revocable source
  delegation. No capability is presented and none needs to still be valid. The
  host re-reads the delegation, re-checks it against current facts, and mints a
  fresh short child from it.

They are two fields rather than a `oneof`: moving a published field into one
changes its presence and is a breaking contract change, which this surface does
not get to make. The exclusivity is a message-level validation rule instead, so
it is still the contract — a request setting both, or neither, is
`INVALID_ARGUMENT` before any handler sees it.

The second is how work longer than any Work Context stays authorized: the
caller keeps an identifier, never a token, and exchanges again for each call.
Because that exchange depends on nothing the caller holds, it succeeds after the
previous child has already expired — which is the whole of the renewal path for
work in submit mode. Nothing is renewed; a fresh capability is minted under a
live re-check, and `WorkContextService.RenewWorkContext` stays parent-token-only
and is not the tool for this.

A reference is an identifier, not a bearer capability, and one fact makes that
true: a delegation is a grant to one binding of one module, and that binding
declares the audience it may call. **Only the module named as that audience may
present the reference.** The delegating module itself cannot, and neither can
anyone who merely learns the id; both are refused as `PERMISSION_DENIED`,
indistinguishably from an id that names nothing. This is the same link the
parent-token arm enforces by requiring the parent's audience to equal the
caller's prefix — read from the delegation directly, because there is no parent
to read it from.

`binding_id` names the **caller's own** installed binding, not the delegation's.
The child's audience is that binding's, and its actor scopes are that binding's
invoke scopes — or its read-only lookup subset — intersected with what the
person actually delegated. A lookup the delegation does not cover is refused
rather than issued empty. The capability is otherwise indistinguishable from
what the parent-token arm produces: owned by the person, one actor hop carrying
the delegating module's principal and the delegation id, sealed to the same
revision, so `CheckAuthorizationRevision` confirms it and a revoke stops
confirming it at the next hop.

A revocation takes effect on the **next call**. Work already in flight is not
recalled — this is a live re-check before each mint, not an atomic cross-service
fence — so a run whose delegation is revoked mid-flight closes partial.

An operation binding declares canonical invoke scopes and a read-only lookup
subset:

A call may return to an earlier audience to read that owner's retained record.
Only exchanging to the immediate parent's audience is refused. Every intervening
binding must preserve the required read scope, including receipt-lookup paths;
the return binding cannot recover authority removed by an earlier exchange.
Use a separate read-only binding for that proof rather than a task-start or
execution binding. The signed owner, tenant, task and session remain unchanged,
and current revocation is checked again on the return exchange.

```json
{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"generate":{"audience":"example-producer","invoke_scopes":[{"resource_kind":"results","actions":["read","write"]}],"lookup_scopes":[{"resource_kind":"results","actions":["read"]}]}}}}
```

Resource kinds, actions and resource ids are sorted and unique; wildcards are
refused. Lookup scopes must be a subset of invoke scopes and may contain only
`read`. Removing or changing the installed binding revokes the exchange on the
next call, including the post-signing policy recheck. Every attempt emits a
credential-free `saas.module.delegated_audience_exchange` observation (v2) with
the effective actor, the authenticated module, the binding, the selected
audience when known, the grant reference when one was presented, and an `issued`
or sanitized `refused` outcome.

The owner is recorded whenever one has been identified, which is every case past
the parent check and every issued mint. It is **absent** in exactly two: a
parent that does not verify, and a reference that resolves to no delegation.
Both name nobody, and both are kept rather than dropped for want of a name —
they are the refusals a probe produces, and the reference or the module that
presented it is what identifies them.

## A worked installed operation binding

Two composed modules: `example-consumer` runs work on a person's behalf,
`example-producer` owns an invocable resource. The composition wants the first
to reach the second for one exact resource and nothing else.

`example-producer` contributes the vocabulary first — a
`permissions.codefly.yaml` validated by
`contracts/permissions/v1/permissions-contribution.schema.json`, naming the
resource and the actions it governs. The host never invents that vocabulary; a
resource kind nobody contributed is a string the composition made up.

```yaml
schema: codefly/saas/permissions-contribution/v1
namespace: example-producer
permissions:
  - name: example-producer.profiles:invoke
    resource: example-producer.profiles
    action: invoke
  - name: example-producer.profiles:read
    resource: example-producer.profiles
    action: read
```

A permission every ordinary member of an organization should hold, such as
reading their own records, adds `members: true`. That member holds it as a
person with no role assignment; a delegated agent actor never does. Owners and
admins already hold every permission. See `AUTHZ.md` "Built-in role catalog
import".

The composition then declares the binding on the **calling** principal in
`module-capabilities/MODULE_PRINCIPALS`:

```json
{
  "example-consumer": {
    "tenant": "019f6bf7-5b4b-74e5-8c17-092259bb1661",
    "operation_audiences": {
      "run": {
        "audience": "example-producer",
        "invoke_scopes": [
          {
            "resource_kind": "example-producer.profiles",
            "actions": ["invoke", "read"],
            "resource_ids": ["standard"]
          }
        ],
        "lookup_scopes": [
          {
            "resource_kind": "example-producer.profiles",
            "actions": ["read"],
            "resource_ids": ["standard"]
          }
        ]
      }
    }
  },
  "example-producer": {
    "tenant": "019f6bf7-5b4b-74e5-8c17-092259bb1661",
    "resources": ["example-producer.profiles"]
  }
}
```

`example-consumer` redeems it with `ExchangeDelegatedOperationAudience`,
supplying its retained parent, the binding id `run`, and whether this is an
invoke or a lookup. It supplies no audience, scope, action, resource id or
lifetime: every one of those is read from the entry above. The parent must
address `example-consumer`'s own installed prefix and tenant, and must already
admit the installed scopes — installation attenuates a viewer's authority, it
never confers new authority.

Three properties of the entry are worth reading twice. `audience` may not equal
the caller's own prefix, so a binding cannot be a self-grant. `resource_ids`
pins the exact resource; omitting it widens the child to every resource of that
kind, and `"*"` is refused rather than treated as a wildcard. And because the
binding is read on every call rather than captured into a child at exchange,
deleting the `run` entry stops the exchange as soon as the deployment carrying
that deletion is serving — including in the policy recheck that runs after
signing — rather than when the last issued child expires.

A resource id is an opaque string, and the producing module owns what its ids
mean. The host neither derives nor checks them: the producer's contribution
declares resource kinds and actions and cannot name an id at all, and no
registry of ids is consulted, so an id no producer recognises parses, signs, and
reaches nothing until the producer refuses it. Beyond the sorting and wildcard
rules above, the host requires each id to be non-empty, free of surrounding
whitespace, and at most 512 characters — a bound a digest is always inside and a
hand-written identity is not — and then compares it byte for byte. An id that
breaks one of those is refused while the registry is parsed, which fails
accounts' start-up rather than one call.

Name the resource by its **stable identity**, not by a digest of its current
definition. `MODULE_PRINCIPALS` is parsed once, at accounts start-up, so
re-issuing this entry is a deployment rather than an edit: a grant that has to
be re-issued whenever the producer's definition moves leaves the consumer
without access from the moment that definition moves until every accounts
process is serving the new entry, and while an old and a new process both serve,
the same context is confirmed by one and refused by the other. For a module that
also declares `headless_scopes`, the same re-issue revokes every operation
context it has minted, across all of its bindings — the whole-entry revision
under [Operation contexts with no person
present](#operation-contexts-with-no-person-present). Children exchanged from a
person's parent, like the one above, are owned by that person and take the
row-backed check instead, so an entry change does not revoke them; they expire
within the minute on their own.

Pinning a definition is the producer's own check, made on each request against
what it has installed. The host makes no such check, so a producer that means a
grant to cover one exact definition enforces that itself: the id here says which
resource is meant, never which version of it.

## Operation contexts with no person present

Both exchanges above need a parent: somebody signed in, and the child is
attenuated from their authority. Background work has no such parent — a module
re-embedding its change journal through another module's service at 3 a.m. is
acting for itself, not for a viewer. `ModuleCapabilitiesService.MintModuleOperationContext`
is that case, and it is deliberately a separate, narrower grant rather than a
relaxation of the delegated exchange.

The module authenticates exactly as for its own Work Context — the identity
secret for its prefix, checked against `federation/MODULE_IDENTITY_SECRETS` —
and names one of its installed operation bindings. Nothing else is requestable.
The host mints a child Work Context with:

- `aud` = the binding's `audience`;
- authority scopes and the actor's granted scopes = **exactly** the binding's
  `headless_scopes`;
- owner and sole actor = the module's own service principal (kind `service`);
- tenant = the tenant `MODULE_PRINCIPALS` declares for that principal;
- a 60-second lifetime and the idempotent replay policy;
- an authorization revision that is a digest of the module's whole
  `MODULE_PRINCIPALS` entry (`business.ModuleOperationContextRevision`, never
  zero).

A consumer confirms such a context with `WorkContextService/CheckAuthorizationRevision`
like any other. A module principal has no row-backed authority to resolve, so
accounts answers from the declaration instead: the owner must be a declared
module principal, the tenant its declared tenant, the revision the digest of its
**current** entry, every subject the module principal itself (as owner and as
its sole actor hop), and every subject's scopes within one binding's
`headless_scopes`. Any mismatch is `PermissionDenied` — a denial, never an
outage a consumer should retry. Because the revision covers the whole entry, any
change to it (once the deployment picks it up) revokes every outstanding
operation context of that module. Contexts owned by a person or an installation
take the row-backed check unchanged.

`headless_scopes` is an optional list on each operation binding, in the same
canonical shape as `invoke_scopes`, and it must be a subset of them — acting
alone, a module can never do more with an audience than it could on a person's
behalf:

```json
{"documents":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"model":{"audience":"modelservice","invoke_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"],"resource_ids":["<profile name>"]}],"lookup_scopes":[{"resource_kind":"modelservice.profiles","actions":["read"],"resource_ids":["<profile name>"]}],"headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"],"resource_ids":["<profile name>"]}]}}}}
```

Here the module may invoke and read that one profile both on a person's behalf
and alone; a binding that should do less alone lists fewer actions or resources
under `headless_scopes`.

The rules are fail-closed on purpose:

- A binding with **no** `headless_scopes` cannot be minted headless at all.
  `invoke_scopes` describe authority exercised on a person's behalf and are never
  reused implicitly for work that has none. An empty list is a configuration
  error, not a second spelling of "absent".
- The binding is re-read on every mint, so removing `headless_scopes` stops the
  next mint; an issued context lives at most a minute.
- A binding addressed to `module-capabilities` — the capability surface itself —
  is refused, so this path can never mint a second spelling of the module's own
  identity.
- Every issuance emits `saas.module.operation_context_minted` with `prefix`,
  `tenant`, `binding_id`, `audience` and the granted `scopes` (one
  `kind:action[:resource]` entry per grant). The record is written once the
  capability exists, and the capability is withheld if it cannot be committed.

A composed module reaches it through the gateway's `POST
/modules/_operation-context` ([services/auth-gateway/AGENTS.md](./services/auth-gateway/AGENTS.md)),
which brokers to accounts exactly as `/modules/_work-context` does.

## Operation contexts from a source delegation

A datasource source's sync is neither of the cases above. It runs as a module
task — scheduled, triggered by a provider webhook, or started by a person — with
nobody signed in, yet it must not run on the module's own authority: it reads
and writes one organization's content because a person in that organization
connected the source. `ModuleCapabilitiesService.MintSourceOperationContext`
turns that act of connecting into the authority the task runs with.

**The delegation is recorded at connect time.** When an owner or admin of an
organization connects a source (`AddSource`, `AddGitHubSource`) or reconnects
it (a replacement credential on `SyncSource`, `MigrateGitHubSourceToApp`), the
host records, in the same transaction, one `source_delegations` row per composed
module that accepts delegations there: the source, the organization, the person,
the module's prefix, the one operation binding that accepts delegations, and a
digest of that binding's audience and delegation scopes. A reconnect revokes the
previous row (`replaced`) and records a new one under the reconnecting person,
atomically. Sources connected before a module declared a binding carry no
delegation; nothing backfills one, so a person must reconnect them.

**Which module and binding is declared, never requested.** An operation binding
accepts delegations by declaring `source_delegation_scopes` — like
`headless_scopes`, an optional list in the canonical scope shape that must be a
subset of its `invoke_scopes` (the context acts on a person's behalf). At most
one binding of a module may declare it, and a connect delegates to that binding
of every module that declares one. The declaration is the whole opt-in:

```json
{"docstore":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"source-sync":{"audience":"docstore-ingest","invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}}}
```

**The mint.** The module authenticates exactly as for its own Work Context and
names either a `delegation_id` or a `source_id` (whose active delegation to the
calling module is used). The tenant, the owner, the audience and the scopes all
come from the delegation. Every mint re-checks, failing closed on each: the
caller is the module the delegation names; the delegation is active; the binding is still
declared, still accepts delegations, and is unchanged in audience and scopes;
the source exists; the person is still a member, still an owner or admin (the
role connecting a source requires), and their account is active. The child is:

- `aud` = the binding's `audience`, never `module-capabilities`;
- tenant = the source's organization;
- owner = the person; sole actor = the module's service principal (kind
  `service`), whose hop carries the delegation's id;
- authority scopes and the actor's granted scopes = **exactly** the binding's
  `source_delegation_scopes`;
- a 60-second lifetime and the idempotent replay policy;
- an authorization revision that binds the delegation row, the binding digest
  and the person's authorization revision in the organization
  (`business.SourceDelegationContextRevision`, never zero).

**Revocation.** A delegation ends when an organization administrator revokes it
(`DatasourceService/RevokeSourceDelegation`), when the source is reconnected, when
the source is deleted, when the person is removed from the organization, and
when the person is left without an owner or admin role. The service paths for
the last three mark the row in the event's own transaction. None of that is
relied on: the mint re-checks every fact, and on finding one false it revokes
the row with the reason (`source_deleted`, `member_removed`, `permission_lost`,
`user_inactive`, `binding_changed`), records it, and refuses. A row is never
deleted; `DatasourceService/ListSourceDelegations` shows administrators who
delegated what to which module binding, and when and why it ended.

**Revision check.** `WorkContextService/CheckAuthorizationRevision` recognises a
context owned by a person and actored by a declared module principal, and
confirms it only while the module holds an active delegation from that owner in
that organization whose binding is unchanged, whose source exists and whose
person still holds the connect role, whose revision recomputes to the sealed one
at the person's current authorization revision, and whose delegation scopes
cover both subjects. Revoking or replacing the delegation, a membership or role
change, or a binding change therefore invalidates outstanding contexts at the
next hop. Every refusal is `PermissionDenied`.

**The delegation authorizes the organization; `cross_tenant` is never
consulted.** Neither the recording, the mint, the revision check nor the
exchange below reads a module's `tenant` or `cross_tenant`. A module bound to
one tenant mints for a delegation in another; a module holding `cross_tenant`
gains nothing here, and a principal gains no cross-organization reach from a
delegation beyond the one binding it names.

**Exchanging a delegation-bearing parent.** The minted context is addressed to
the binding's audience, which may be another composed module (a runtime that
executes the sync). That module passes it to
`ModuleCapabilitiesService/ExchangeDelegatedOperationAudience` as the parent,
naming one of its own operation bindings, as for any delegated parent. When the
verified parent's single actor hop is a declared module principal carrying a
delegation id, the exchange re-checks that delegation exactly as the revision
check does — active, in the parent's tenant, from the parent's owner, to that
module, binding unchanged, source present, person still an owner or admin, the
sealed revision recomputing — and that delegation, not the caller's declared
tenant, admits the parent's tenant; the id in the token is only a pointer. The
rest is unchanged: the parent must be addressed to the caller's prefix, the
binding must be one the caller declares, and the child is attenuated to that
binding's scopes, which must lie within the parent's. The child keeps the
parent's owner, actor hop and revision, so the revision check confirms it the
same way and stops the moment the delegation ends. A parent with no module
actor hop keeps today's tenant check (`cross_tenant` or the caller's own
tenant) exactly; a read exchange always does.

**Codes** (on the RPC, with a `google.rpc.ErrorInfo` under the domain
`accounts.saas.codefly.dev`, and relayed by the gateway):

| Answer | gRPC code | ErrorInfo reason | Gateway | Meaning |
| --- | --- | --- | --- | --- |
| Missing | `FAILED_PRECONDITION` | `DELEGATION_MISSING` | `412`, body `DELEGATION_MISSING` | The source has no active delegation to this module. A person must connect or reconnect it. |
| Revoked | `PERMISSION_DENIED` | `DELEGATION_REVOKED` | `403`, body `DELEGATION_REVOKED` | The delegation was revoked, or was just found unsupported and revoked. |
| Invalid | `PERMISSION_DENIED` | `DELEGATION_INVALID` | `403`, body `DELEGATION_INVALID` | No such delegation for this module — another module's is deliberately indistinguishable from one that does not exist. |
| Unproven | `UNAUTHENTICATED` | — | `401` | The module's identity secret was not accepted. |

The gateway's refusal body is the plain-text reason, read from the
`ErrorInfo` and never from the message, and it relays only the three reasons
above on their own status: a `PERMISSION_DENIED` with no reason, another
reason, or another domain is a bare `403` with body `forbidden`, so the gateway
never says more about a delegation than accounts decided to.

**Audit.** `saas.datasource.delegation.created` on connect and reconnect,
`saas.datasource.delegation.used` on every mint (written once the capability
exists; the capability is withheld when it cannot be written, or when the
delegation was revoked while it was being signed), and
`saas.datasource.delegation.revoked` with its `reason` on every revocation.

**What a consuming module does.** Mint once per task admission — by `source_id`,
which the sync job it was handed already names — and present the context to the
binding's audience; mint again rather than holding one past its minute. Read
`412`/`DELEGATION_MISSING` as "this source needs a person to reconnect it" and
surface it that way; read `403` as the same outcome for a delegation that has
ended (`DELEGATION_REVOKED`) or that this module may not use
(`DELEGATION_INVALID`). Never fall back to the module's own authority.

The gateway exchange is `POST /modules/_source-operation-context`
([services/auth-gateway/AGENTS.md](./services/auth-gateway/AGENTS.md)).

The transport all of this is carried on, and what a client library must and
must not require of it, is [INTERNAL_TRANSPORT.md](./INTERNAL_TRANSPORT.md).

## Callable-source declarations

`datasource:read` and `datasource:invoke` are source-boundary role permissions.
Reading declarations rechecks organization membership, the source boundary's
`datasource:read` grant, and personal-source ownership. An administrator atomically
replaces declarations; neither a declaration nor its digest grants authority.
The generic authorization-code source remains personal even when another member
holds an organization-wide wildcard role. OAuth token custody is independent of
the host's inbound Work Context and delegated-audience exchange.


Callable source operations use `saas-datasource`, the host's source-operation
audience. `module-capabilities` remains the module identity audience and the mint
explicitly refuses it as an operation audience; there was no existing host call
audience to reuse. The new owner is included in the closed host audience set.
The existing `operation_audiences` registration and
`ExchangeDelegatedOperationAudience` perform issuance and attenuation; no second
exchange is introduced. Source-delegation parents are rechecked through
`ConfirmSourceDelegationParent`, including the current binding digest. Ordinary
delegations use the existing chain journal and authorization revision recheck.

The Runnable `source` slot requires invoke/read and read-only recovery. An
installation supplies the `datasource.sources` kind and explicit source IDs for
that slot. The host maps each source ID to its boundary node and checks
`datasource:invoke` or `datasource:read` there; an unscoped source-kind grant is
refused. Personal OAuth sources also require the owning person. Declarations
and receipts cannot expand these scopes. Invoke rechecks again after waiting for
the SDK effect lock, before a committed response can be replayed.
