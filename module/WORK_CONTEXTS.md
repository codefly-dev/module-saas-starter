
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

A platform super_admin declares an optional `read_audiences` map in the
principal's `grant` in the module envelope (`PlatformAdminService/PutModuleEnvelope`),
for example:

The envelope is written by a platform super_admin with fresh AAL2 evidence, or by an API key carrying `platform.module_envelope:write` and nothing else. A key has no second factor and never will, so that scope stands in its place: it names this ceiling alone, a key cannot mint another key, and the key's owner must still hold super_admin. Without it the ceiling was writable from a browser and nowhere else, which left the one record the design calls dynamic with no operator surface at all.

```json
{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","read_audiences":{"proof":{"audience":"example-producer","scopes":[{"resource_kind":"results"}]}}}
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
the **caller's** principal — the module that uses the capability, whose running
build core attests — and the delegation id, with the delegation sealed as its
operation binding (below), so every verifier reads the same live binding and a
revoke refuses it at the next hop.

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
execution binding. The signed owner, tenant and task remain unchanged, the
returned capability opens a fresh session (a session a capability carries is its
minter's verified session or new, never copied), and current revocation is
checked again on the return exchange.

```json
{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"generate":{"audience":"example-producer","invoke_scopes":[{"resource_kind":"results","actions":["read","write"]}],"lookup_scopes":[{"resource_kind":"results","actions":["read"]}]}}}}
```

Resource kinds, actions and resource ids are sorted and unique; wildcards are
refused. Lookup scopes are optional: a binding that declares only invoke scopes
redeems no lookup, and a lookup exchange under it is refused by name. When present,
they must be non-empty, a subset of invoke scopes, and may contain only `read`.
Removing or changing the installed binding revokes the exchange on the
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

A platform super_admin then allows the binding for the **calling** principal in
its `grant` in the module envelope:

```json
{
  "revision": 4,
  "principals": {
    "example-consumer": {
      "grant": {
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
      }
    }
  }
}
```

The producer's own principal lists `example-producer.profiles` under its
grant's `resources`. The consumer's registry entry is this grant bounded by the
`authority` its applied module manifest asks for: the `run` audience exists only
while the manifest asks for the `run` binding on audience `example-producer` with
an `invoke` operation, and each operation keeps only the actions the manifest
asks for (`example-producer.profiles:read`, …) — `resource_ids` stay the
envelope's, because a manifest cannot name one.

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
removing the `run` entry from the envelope stops the exchange as soon as the
next reconcile pass installs the registry without it — including in the policy
recheck that runs after signing — rather than when the last issued child
expires.

A resource id is an opaque string, and the producing module owns what its ids
mean. The host neither derives nor checks them: the producer's contribution
declares resource kinds and actions and cannot name an id at all, and no
registry of ids is consulted, so an id no producer recognises parses, signs, and
reaches nothing until the producer refuses it. Beyond the sorting and wildcard
rules above, the host requires each id to be non-empty, free of surrounding
whitespace, and at most 512 characters — a bound a digest is always inside and a
hand-written identity is not — and then compares it byte for byte. An id that
breaks one of those is refused when the envelope is written, so the write is
rejected with the reason rather than one call failing later.

Name the resource by its **stable identity**, not by a digest of its current
definition. The module principal registry is derived, each reconcile pass, from
the applied module manifests and the module envelope, so re-issuing this entry is
an envelope revision. A grant that has to be re-issued whenever
the producer's definition moves leaves the consumer without access from the
moment that definition moves until the new revision and its deliveries are
applied, and each accounts replica derives its own registry on its own pass, so
for up to one pass the same context is confirmed by one replica and refused by
another. For a module that
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

## The capability itself: core/workcontext

Every Work Context this host issues is signed by `core/workcontext`'s
`Authority` and every one it reads back is checked by core's `Verifier`; the
gateway edge holds a presented one to core's `Inspector`. There is no second
encoding: core refuses a token in any other format as `ErrNotACoreToken` before
it reads a field. What the host owns is the live state core asks an issuer for,
answered by one set of functions for minting and verifying alike
(`pkg/business/work_context_seal.go`):

- **Seal.** Every capability — a person's as much as a module's — is sealed to an
  installation and its revision. A person's capability for a module or a solution
  is sealed to the organization's installation of it, held while the person is an
  active member; an organization that never installed the audience is minted
  nothing. A module's is sealed to the installation its mint resolved.
- **Epoch.** The owner's authorization revision in the organization. Membership
  and role changes advance a person's; installing a module and every change to
  its installation's revision advance a module principal's (migration 35).
- **No build.** A Work Context seals no build (core #812): a module's mint is
  bound by `BindExecution` to a workload its applied manifest lists, and what a
  workload runs is deploy-time admission.
- **Revision.** The greater of the organization's and the owner's revision for a
  person; a module principal's is `ModuleOperationContextRevision` of its
  installed grant. Core keys its revision source by tenant, so the host binds it
  to the capability's owner as well.
- **Session.** Every session a capability carries is the session its caller
  authenticated with, or a fresh one — never one a request names. `StartTask`
  binds the caller's verified session; `StartChildSession`,
  `StartInstallationTask`, a root session and a renewal assign a fresh one and
  return it in the response. The gateway pairs a resource-confined credential
  with a capability on that session alone, which holds only because a caller can
  never choose it.

Core derives only by adding a hop and never extends a window, so re-issuing a
capability — a root session, an audience exchange, a renewal — mints it afresh
from its owner and re-derives each hop, with the same delegation ids, against
live state. The seal is re-resolved too, never copied from the parent: a
person's capability is sealed to the organization's installation of the
audience it now addresses (an exchange into an audience the organization never
installed is refused), and a module or installation principal's to its own
installation. A parent this host derives from is verified at full strength for
the audience it names, and a single-use parent is consumed by the derivation, as
by any consumer. A capability presented with a QUESTION — a viewer's capability
asked what it may read (`CheckWorkContextRecordAccess`,
`ListReadableSourceCollections`), a module's parent asked about its installation
or artifacts — is read back instead (core's `Authority.ReadBack`): every check a
verifier makes, against live state, but the replay store is never touched. A
question is not a use, so the consumer that verified and consumed a single-use
capability can still ask about it, on every page of a listing, and asking leaves
an unconsumed one for its consumer. A read-back trusts only the issuer's CURRENT
signing key: during a key rotation, a capability signed under the previous key
is refused by every read-back (fail closed) even while a consumer still accepts
it, until its holder mints a new one.

## A callee verifies at full strength: the live authority a module reads

A composed module that RECEIVES a Work Context is its callee, and the checks it
can make from a trust root alone are a forwarding hop's: the encoding, the
signature, the issuer, the audience, the window and the shape. A hop consults no
live state and consumes nothing, so a module that stops there accepts a
capability whose installation was revoked, whose build was replaced, whose
operation binding was withdrawn or whose principal's epoch has moved — for as
long as that capability lives, which is at most the 15-minute ceiling but is
still every use inside it.

The missing half is the issuer's live state, and this host is the issuer. It
serves it on the endpoint a module already depends on:
`ModuleCapabilitiesService/ResolveWorkContextAuthority` (`EXPOSURE_INTERNAL`, on
`accounts/authority`), one question per call, each the exact question core's
`Verifier` asks its `SealSource`, `RevisionSource` or `GrantSource`:

| Question | Answers | Absence |
| --- | --- | --- |
| seal | the live installation and its revision, for the capability's owner | `UNHELD` — not active in the organization, or the principal is not a member |
| principal epoch | one principal's live epoch, for the owner and every actor hop alike | `UNKNOWN` — no authorization record here |
| operation binding | the binding the capability is sealed to, exactly | `ABSENT` — no such delegation, or one this organization does not entitle |
| authorization revision | the revision a capability owned by that principal carries | — |
| approval grant | the approval the capability's grant hop claims | `NOT_RECORDED` — this host records none |

### The capability is the authorization

Every question carries the capability it is about, and that is what bounds this
surface. The host **authenticates** it — core's `Inspector`: encoding, issuer,
signature under this issuer's key, window, shape, no live state read and nothing
consumed — against the **route target the host resolved from the CALLER'S OWN
credential**, which is a module's registration prefix and the route alias its
delivered presence declares. A capability addressed to anything else is refused.
Then the capability's tenant must be the organization the caller's credential is
bound to, and each question's identifiers must be ones the capability names: the
owner, a principal in its actor chain, the installation it is sealed to, the
binding it exercises, the grant its hop claims. They travel in the request and
are **checked**, not inferred, so a client asking about something else is told so
rather than quietly answered about the capability's own values.

That shape is the correction an independent review forced, and the reasoning is
worth keeping. The first revision took the tenant and the audience as request
fields and compared the audience to the caller's prefix. No **answer** depended
on either field, so the comparison pinned nothing: a module could label the call
with its own prefix and still read any principal's epoch or authorization
revision in its tenant, which made the strongest check in the model
a label. A presented, authenticated capability has nothing left to label.

Consequences worth stating:

- A module installed in several organizations asks with the credential it minted
  for the one in question.
- `NOT_RECORDED` is unreachable through this surface today rather than merely
  unused: this host mints no grant hop, so no capability it issues carries a
  grant to ask about. The arm exists so that the day it records approvals, the
  answer has a shape and a client that does not understand it refuses.
- A module addressed by a name that is neither its prefix nor its alias — a
  binding's declared `audience` is a name the module manifest chooses — cannot
  verify at full strength. That is deliberate: accepting a third name would mean
  believing a caller's claim that an audience is its own.

### One body with the issuer

The serving path hands each question to the very sources `coreAuthority` mints
with and `coreVerifier` verifies with (`moduleAuthoritySources` in
`pkg/adapters/work_context_core.go`, over `pkg/business/work_context_seal.go`).
Two constructions of "the current seal" would agree only by coincidence, and the
seal's whole value is that a callee's verdict and an issuance cannot disagree.

### A refusal and an outage are different answers

- `FAILED_PRECONDITION` — the issuer **decided**, and the decision is no: the
  owner is no longer a member, the principal has no authorization record, a
  module's installation is inactive, the delegation behind a binding is stale. The capability is revoked; its holder mints
  again and does not retry.
- `UNAVAILABLE` — nothing was decided. A lookup failed; a retry may succeed.
  A source delegation whose binding is not visible in the view a call resolved
  is this, not a decision: the organisation's consent may not carry the binding,
  or the registry may have been replaced mid-call, and neither says anything
  durable about what a person delegated. **Only a binding that is still declared
  and genuinely different revokes the delegation.** Conflating the two destroyed
  a grant nine seconds after it was recorded, with an unchanged digest, and
  every retry recreated it for the next call to destroy.
- The three absence states above are neither: a verifier turns each into a
  refusal of the capability. An `UNSPECIFIED` state is a host that said nothing,
  which a client refuses on.

Collapsing the first into the second is the defect that review found: every plain
error became `UNAVAILABLE`, so the removed-member case — the **first** question
core's verifier asks, before the seal — reached a callee as a transient failure,
which a retry policy retries forever and a stale-verdict cache serves as "still
good". No refusal carries the cause's own message, because these questions are
about a third party while the authority layer's messages are written for a caller
asking about itself; one of them names the subject's current tenant. The cause is
logged by the host.

### What a module brings, and what it costs

The replay store is deliberately not served. Single-use consumption is the
consumer's own, and an issuer that consumed on a callee's behalf would burn the
nonce to answer a question — so a module brings its own, and a deployment with
more than one replica needs it **durable and shared** or single-use is not single
use. Core's `RevisionSource` is keyed by tenant alone while this host's revision
is owner-dependent, so the question names the owner and a client binds it per
capability.

One verification is **five or more round trips** (revision, seal, a build and an
epoch for the owner and for each hop, a binding), each one an authenticated
capability and a live grant re-read on the host. That is the price of the
guarantee and it is not hidden: a callee that cannot pay it per request should
decide its cadence deliberately rather than discover it from a latency graph.
There is no batch question today.

A module builds `sdk-go/workcontext`'s `PinnedVerifier` on these three sources
plus its replay store; the key set is the JWKS accounts already publishes at
`/v1/auth/.well-known/jwks.json`.
`TestAComposedModuleVerifiesAHostCapabilityAgainstTheServedSources` and the
refusal tests beside it (`pkg/adapters/module_work_context_authority_test.go`)
are that verifier against this surface: an unheld installation, a stale epoch, a
stale revision, a removed owner, an unknown principal, a replaced build and a
withdrawn binding are each refused through it, and a module presenting a
capability addressed elsewhere — or to it in another organization — resolves
nothing.

## The reserved module audience

`module-capabilities` is the audience a composed module's own capability carries,
and every peer that verifies it reads a context bearing it as "a module, with no
person present". That premise is what lets the module-facing surface skip the
questions it asks of a delegated actor.

So the audience is mintable only from a module identity. **Every mint and
exchange that takes its audience from a caller's request refuses it** — the task
mint, the installation mint, a root or child session, the audience exchange and a
renewal. The refusal is the structural half of `requireMintableAudience`, which
needs no state and therefore holds through a registry outage.

On the four mints a signed-in person reaches it runs before the caller's
authority is resolved, so the spelling is unreachable rather than merely
unreachable for this person. The other two reach it differently, and each for a
reason that is the point: the installation mint demands an internal service
credential first, so it is not a person-driven mint at all; and a renewal gates
the audience it RESOLVES, after authenticating, because a renewal naming none
inherits the parent's — gating the request field would let the reserved spelling
travel one generation further while the request named nothing.

The module mints are unaffected: they pass the audience themselves rather than
reading it off a message, as does the internal delegated-read exchange, whose
audience comes from a declaration. `TestPersonMintsRefuseTheModuleAudience` and
`TestEveryRequestSuppliedAudienceRefusesTheReservedOne` hold this, the second so
a mint added later cannot skip the refusal silently.

## Operation contexts with no person present

Both exchanges above need a parent: somebody signed in, and the child is
attenuated from their authority. Background work has no such parent — a module
re-embedding its change journal through another module's service at 3 a.m. is
acting for itself, not for a viewer. `ModuleCapabilitiesService.MintModuleOperationContext`
is that case, and it is deliberately a separate, narrower grant rather than a
relaxation of the delegated exchange.

The module authenticates exactly as for its own Work Context — its pod's
projected ServiceAccount token (audience `accounts`), which `BindExecution` holds
against the workloads its principal's applied manifest lists (#1056) — and names one of its installed operation bindings. What the
binding requires (one principal per ServiceAccount, every container of the pod
approved, a running regular container in a running pod, a token of at most 600
seconds) and the replay it does NOT close are stated in
[services/accounts/AGENTS.md](services/accounts/AGENTS.md#establishing-which-workload-a-caller-is). Nothing else is requestable.
The host mints a child Work Context with:

- `aud` = the binding's `audience`;
- authority scopes = **exactly** the binding's `headless_scopes`;
- owner = the module's own service principal (kind `service`), acting directly:
  no actor hop, as core models an owner acting, with the seal naming its
  installation;
- tenant = the tenant the module envelope's grant declares for that principal;
- a 60-second lifetime and the idempotent replay policy;
- an authorization revision that is a digest of the module's whole derived
  registry entry (`business.ModuleOperationContextRevision`, never zero).

A consumer confirms such a context with `WorkContextService/CheckAuthorizationRevision`
like any other. A module principal has no row-backed authority to resolve, so
accounts answers from the declaration instead: the owner must be a declared
module principal, the tenant its declared tenant, the revision the digest of its
**current** entry, every subject the module principal itself (the owner; the
context carries no actor hop), and every subject's scopes within one binding's
`headless_scopes`. Any mismatch is `PermissionDenied` — a denial, never an
outage a consumer should retry. Because the revision covers the whole entry, any
change to it (once a reconcile pass installs it) revokes every outstanding
operation context of that module. Contexts owned by a person or an installation
take the row-backed check unchanged.

`headless_scopes` is an optional list on each operation binding, in the same
canonical shape as `invoke_scopes`. Beside `invoke_scopes` it must be a subset
of them — acting alone, a module can never do more with an audience than it
could on a person's behalf:

```json
{"documents":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"model":{"audience":"modelservice","invoke_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"],"resource_ids":["<profile name>"]}],"lookup_scopes":[{"resource_kind":"modelservice.profiles","actions":["read"],"resource_ids":["<profile name>"]}],"headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke","read"],"resource_ids":["<profile name>"]}]}}}}
```

Here the module may invoke and read that one profile both on a person's behalf
and alone; a binding that should do less alone lists fewer actions or resources
under `headless_scopes`.

A binding may also be **headless-only**: `headless_scopes` and no
`invoke_scopes` at all — background work no person ever asks for, such as
recording evidence, exporting spans or embedding (#1156):

```json
{"documents":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"embed":{"audience":"modelservice","headless_scopes":[{"resource_kind":"modelservice.profiles","actions":["invoke"],"resource_ids":["<profile name>"]}]}}}}
```

It is the narrower grant, not a looser one: the binding holds no authority a
person can exercise through it. It carries no `lookup_scopes` and no
`source_delegation_scopes` — both act as a person and are bounded by an invoke
grant it does not have, so declaring either beside no invoke is refused. Every
person-delegated exchange (`ExchangeDelegatedOperationAudience` with a parent
token, a delegation-bearing parent, or a delegation reference) refuses it by
name with `PermissionDenied` ("… is headless-only …"), and a confined
credential's ceiling never counts it as declared. Only this mint uses it. The
envelope ceiling may grant an audience headless-only in the same shape. The
reconciler derives a headless-only binding whenever the manifest's headless
scopes survive the envelope and none of its invoke scopes do — it asks only
`headless` operations, or asks invoke the envelope does not allow, or the
envelope's audience is headless-only — and logs the omitted reason when nothing
is left. Where invoke does survive, headless must still fit within it, so a
manifest asking invoke `A` and headless `B` is omitted rather than granted
headless alone. An organization installed before the
binding was granted consented to no such binding: like any binding declared
after an install, it reaches that organization when it installs again.

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

**A connect that can record nothing is REFUSED, not reported as done.**
Recording no delegation is correct for a deployment where no composed module
accepts one, and that is the whole answer there. It is a lie when the module
that does accept one is missing from the registry: the registry is derived as
the applied manifests bounded by the envelope, and an envelope that withholds a
binding a manifest asks for leaves that binding out by name. The connect then
recorded nothing, reported success, and every sync afterwards refused with *the
source has no active delegation to this module; a person must connect or
reconnect it* — telling the person to do the thing they had just done. So the
connect, the reconnect and the trigger backfill all refuse
(`FailedPrecondition`) when they would record nothing while the registry is
incomplete: any binding an applied manifest asked for and the pass could not
grant, or a registry fenced behind a newer envelope revision. The refusal names
every withheld binding — this host cannot tell which of them carried
`source_delegation_scopes`, because the envelope that withheld it is what says
so — and a platform super_admin fixes it by allowing that binding in the next
envelope revision. The same reason is on the asking manifest's binding row
(`ListSolutionHostBindings`: `pending_reason`, `pending_since`), which is where
an operator reads a delivery this host could not take whole.

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
  `service`), whose hop carries the delegation's id and the build its pod is
  running, attested by core;
- authority scopes and the actor's granted scopes = **exactly** the binding's
  `source_delegation_scopes`;
- a 60-second lifetime and the idempotent replay policy;
- the person's authorization revision in the organization, as for any
  capability they own;
- the delegation sealed as the capability's **core operation binding**,
  `source-delegation/<delegation id>/<module prefix>`
  (`business.SourceDelegationBindingID`), naming the module that exercises it.
  Every verifier reads that binding live (`WorkContextOperationBinding`): it is
  granted to that module's principal at its installation in the organization,
  it is revoked the moment the delegation is, and it exists only for the
  delegating module or the module its binding names as audience. A delegation
  whose binding has since changed, or whose person no longer holds the source,
  answers no binding at all.

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
context owned by a person, actored by a declared module principal, **and
sealing a binding that names a delegation** (`source-delegation/...`). The
subjects alone do not make a context this check's: a module executing its own
installed operation for a person has exactly that shape and seals its OWN
binding, and claiming those refused every one of them as a stale delegation
when no delegation was involved. Such a context is declined here and takes the
ordinary module-operation checks, which re-read that binding in full. A context
of this shape naming NO binding stays refused here: the binding travels in the
request, so treating its absence as "not a delegation" would let a consumer
that omitted it have a narrowed delegation confirmed against its module's
broader installation. The caller passes the operation binding the context sealed
(`operation_binding_id`),
and the check confirms it only while that binding names the actor, the live
binding read above holds, the delegation is active and from that owner, its
binding is unchanged, its source exists and its person still holds the connect
role, the sealed revision is the person's current one, and its delegation
scopes cover both subjects. Revoking or replacing the delegation, a membership
or role change, or a binding change therefore invalidates outstanding contexts
at the next hop. Every refusal is `PermissionDenied`. A verifier that reads the
binding itself — core's `Verifier`, and the host when it verifies a parent it
issued — refuses a withdrawn delegation as a stale capability
(`FAILED_PRECONDITION`), before any of these checks runs.

**One rule for every module hop.** A module hop NAMES the binding it
exercises, and that binding confirms it. The exchange seals the binding the
caller ran under onto that caller's OWN hop (`remint`, which stamps it only
where the hop's principal is the exchanging module — an agent hop carries
none), and `CheckAuthorizationRevision` resolves it: a binding naming a
delegation is confirmed against that delegation, any other binding against the
module's installation (`ConfirmModuleHopBinding` — the module is still
installed here, the installation still declares that binding, and the hop's
scopes are still within it). A person is confirmed in the authority store,
which is where a person's roles live.

The rule replaces resolution by elimination. A module principal is NOT in the
authority store — it is not a registered agent — so a hop sent there came back
"not found", which the module calling reported to its caller as a revocation
that had never happened: every call an orchestrator makes to an owner it
executes an operation for was denied that way.

No revision is compared for a module hop, deliberately: the revision sealed
into a context is the OWNER's, and the owner's resolver compares it. A module
hop carries no revision of its own, which is why asking the store for one
produced a mismatch rather than an answer.

Where the binding comes from decides how its ABSENCE is read, and the two are
not the same. The parent re-check reads it from the SIGNED parent, so a parent
naming none was minted naming none and is simply not a delegation's. The
revision check reads it from the CALLER's request, where absence could be an
omission, so a context of that shape naming no binding stays refused: reading
it as "not a delegation" would confirm a narrowed delegation against the
module's broader installation.

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
parent's owner, delegation and revision; its actor hop is the **caller** — the
module that now uses the capability, whose build core attests — and its
operation binding names the caller, so the revision check confirms it the same
way and stops the moment the delegation ends. A parent with no module
actor hop keeps today's tenant check (`cross_tenant` or the caller's own
tenant) exactly; a read exchange always does.

**Codes** (on the RPC, with a `google.rpc.ErrorInfo` under the domain
`accounts.saas.codefly.dev`, and relayed by the gateway):

| Answer | gRPC code | ErrorInfo reason | Gateway | Meaning |
| --- | --- | --- | --- | --- |
| Missing | `FAILED_PRECONDITION` | `DELEGATION_MISSING` | `412`, body `DELEGATION_MISSING` | The source has no active delegation to this module. A person must connect or reconnect it. |
| Revoked | `PERMISSION_DENIED` | `DELEGATION_REVOKED` | `403`, body `DELEGATION_REVOKED` | The delegation was revoked, or was just found unsupported and revoked. |
| Invalid | `PERMISSION_DENIED` | `DELEGATION_INVALID` | `403`, body `DELEGATION_INVALID` | No such delegation for this module — another module's is deliberately indistinguishable from one that does not exist. |
| Unproven | `UNAUTHENTICATED` | — | `401` | The module's projected token was refused, or belongs to a workload no applied manifest of the principal lists. |
| Unbound | `UNAVAILABLE` | — | `503` | The host could not establish what the module's pod is running; nothing was decided. |

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
the SDK effect lock, before a committed response can be replayed. Lookup reads
retained receipt evidence under current source read authority even after the
operation declaration is removed or replaced; the declaration is not evidence
of whether an effect was attempted.

Receipt retention uses the same selected source scope and audience through
`PruneSourceOperationReceipts` / `LookupPruneSourceOperationReceipts`, with a
current organization-admin check for shared sources; a personal source instead
requires its owning member. These checks precede invocation and replay. A
composition schedules the Connect Runnable; the host adds no timer or queue.
Expired output never removes a mutation's last effect marker. See
[Callable sources](CALLABLE_SOURCES.md#receipts-and-uncertain-outcomes).

## Triggers: for work no person started

A release may declare what admits work against it besides a person — a schedule
or an arrival — and the declaration is **refused without an identity**. That is
the whole of the problem this section answers: an occurrence has nobody signed
in, and the host has no identity of its own to lend it.

**The declaration, as the executing module validates it.** The host builds it in
`business.DeclaredTriggerSchedule` and `business.DeclaredTriggerIdentity`, whose
JSON field names are the contract and are pinned by test
(`TestTriggerDeclarationMatchesTheContractFieldNames`) — a rename on either side
is a release that will not install rather than a schedule that quietly stops:

```json
{
  "schedule": {
    "cron": "*/30 * * * *",
    "timezone": "UTC",
    "missed_run": "skip",
    "overlap": "skip",
    "deadline": 600000000000,
    "input": {"org_id": "...", "source_id": "..."}
  },
  "identity": {"source": "<source id>"}
}
```

**The names above are the INSTALLATION DOCUMENT's, and that distinction is
load-bearing.** The module that fires triggers spells the same facts two ways:
it decodes an installation with `source` / `binding`, `"skip"` / `"catch_up"`,
`"skip"` / `"queue"` / `"cancel_previous"`, a `deadline` in **nanoseconds** and
`event` for an arrival — and answers an operator's READ with
`source_delegation` / `installation_binding`, `MISSED_RUN_POLICY_*`,
`OVERLAP_POLICY_*`, `deadline_millis` and `arrival`. Only the first has to parse
for a release to install at all, so it is the one this host speaks, in its Go
types, in its own read surface and on its page: what a reader sees is what a
composition pastes in. A declaration carrying the read API's spelling is
refused at install on every field, which is a failure nothing in this host can
observe — hence `TestTriggerDeclarationMatchesTheContractFieldNames`, which
pins the document's names *and values* and records the other column beside
them.

**Exactly one identity, and the two are not alternatives of equal standing.**

- `source` — **the grant reference, and the contract.** It names a
  connected datasource source. On every occurrence the host resolves that
  source's active delegation (`MintSourceOperationContext` by `source_id`) and
  re-checks all of it: the delegation is live, the binding is still declared and
  unchanged in audience and delegation scopes, the source exists, the person is
  still an active member holding the connect role. The capability it answers
  with is owned by that person and actored by exactly one module service
  principal whose hop carries the delegation id, with the delegation sealed as
  its operation binding — the shape a consuming module
  recognises as a grant reference, which is what lets an occurrence's run
  outlive the capability that admitted it. Nothing is cached: a revocation lands
  on the next occurrence, not behind something held from the last one.
- `binding` — **the installation-principal arm, and PLANNED.** It would name
  one of the release's declared bindings and mint from that binding's
  `headless_scopes` under the module's own principal
  (`MintModuleOperationContext`). That principal owns it and acts directly, with
  no actor hop, so there is no delegation to revoke and no authority to outlive
  the token: the run would be bounded by the capability's own short lifetime.

  **It cannot serve the reconcile trigger, and this host has never emitted it**
  (issue #1112). Three facts, each on its own sufficient: `ModuleOperationContextTTL`
  is 60 seconds while the reconcile trigger declares a 10-minute deadline, so an
  occurrence bounded by the capability cannot carry the deadline the declaration
  states; the capability is the module's own principal, while `ReconcileSource`
  requires an organization **member**, which an installation principal is not;
  and `DeclareSourceTrigger` sets `Identity.Binding` nowhere. Making it work
  needs a lifetime policy for occurrences that this mint does not have — a
  second lifetime on a capability every other headless call shares — so it is
  stated as planned rather than quietly shortened to fit.

**What a composition must declare for either arm to mint at all.** The grant is
the opt-in, exactly as for every other operation context: `source_delegation_scopes`
for the reference arm, `headless_scopes` for the principal arm, each a subset of
the binding's `invoke_scopes` (a headless-only binding, above, serves the
principal arm alone). Beside them the binding names the `audience` the
executing module admits under, which for this one case may be the module's own
declared prefix:

```json
{"runtime":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"occurrences":{"audience":"runtime","invoke_scopes":[{"resource_kind":"collections","actions":["read","write"]}],"lookup_scopes":[{"resource_kind":"collections","actions":["read"]}],"headless_scopes":[{"resource_kind":"collections","actions":["read"]}],"source_delegation_scopes":[{"resource_kind":"collections","actions":["write"]}]}}}}
```

**Why a self-audience is allowed here and nowhere else.** Every other operation
binding describes calling *another* service, so a module naming itself as its
own audience was refused outright (`validateOperationAudiences`). Unattended
work inverts the direction: an occurrence's capability is admitted by the module
that executes the release — itself — which attaches it as its own incoming
execution context and verifies it against its own declared name. A composition
that could not name its own prefix would have to make its executing module admit
a second audience it does not accept. `selfAdmittingBinding` therefore narrows
that one guard to bindings declaring headless or delegation scopes, and leaves
the guard that protects identity untouched: a capability addressed to
`module-capabilities` is still refused at signing, whatever a binding declares.

**Binding a reference to a source that is already connected.** A delegation is
recorded only when a person connects or reconnects a source, so a release
installed afterwards finds no reference to name and nothing backfills one.
`DatasourceService/DelegateSourceTriggers` records one under the calling
organization administrator, in one transaction, and answers with the reference
to write into the release's trigger. It grants nothing new: the scopes are the
binding's declared ones and every fact is re-checked on every mint.

It is **additive**, and it **refuses another member's personal source** (issue
#1112). It used to run the connect path, which is right for a connect and
destructive for a backfill: that path revokes every delegation on the source as
`replaced` and re-inserts each one owned by the caller, so one administrator's
press took a personal source's delegation away from its owner and moved every
unrelated module's authority onto the administrator — failing in-flight children's
re-checks and running everything afterwards under the wrong authority. A target
that already holds an active delegation is now left exactly as it is, and only a
target with none gets one. A personal source is its owner's, which is the
division `ReconcileDatasourceSource` makes on the pass itself: it refuses a
personal source to anyone but its owner, so a reference under an administrator
would bind a trigger to an authority that cannot perform the operation it
admits. (`ReconcileSourceAuthority`, the role gate before it, admits the owner
*or* an organization administrator — so it is not the check that draws this
line.)

**Reading it back.** `DatasourceService/GetSourceTriggerDeclaration` answers the
declaration, the references an operator may bind to it, and the children minted
against them. The children are read from `saas.datasource.delegation.used`,
because that trail is the only record of one — a capability lives about a minute
and nothing caches it — so a revoked reference shows up as the next occurrence
having no child at all. The playground that drives the whole walk is
`/admin/platform/unattended-work`.

**What the reconcile trigger's occurrences admit, and the scope that binds
them.** `DatasourceService/ReconcileSource` and `LookupReconcileSource` are
source operations, so a delegated capability authenticates as the **delegating
person** — and a reference is only ever recorded under someone holding the
connect role, which is an organization administrator. Tenant, membership and
role are therefore satisfied for every source in that organization, which is
why those two RPCs bind the capability to the **source id** as well
(`requireSourceOperationScope`, under `invoke` for the pass and `read` for the
receipt lookup). Without it a module holding a child for one source could
reconcile every source in the organization (issue #1112). The action names are
the existing per-source vocabulary on purpose: a new `reconcile` verb would be
one no composition declares in its `source_delegation_scopes`, and would refuse
every real delegation rather than narrow it.

**And the scope check alone is not enough, because the scope list is
per-BINDING.** A mint seals the binding's declared `source_delegation_scopes`
verbatim, and one scope may name up to 64 resource ids — so a child minted from
source A's delegation legitimately carries a scope naming A, B and C. A scope
check admits it on all three, under A's delegator's authority, with B's and C's
delegations never read and their revocation unable to stop it. So
`requireReconcile` also compares the **delegation's own source** with the one
being acted on (`requireDelegatedSourceMatch`), from the delegation
`ConfirmSourceDelegationParent` returns — which the adapter used to discard. The
two checks answer different questions and neither implies the other: "may this
capability act on this source at all" and "is this the source whose owner's
authority it is carrying".

**The ceiling on how many sources one trigger can serve** is therefore the
declared scope's, not a deployment variable's. A binding declares at most one
scope per `resource_kind` (kinds must be strictly increasing) and at most 64
resource ids in it, so a module's trigger reaches at most **64 named
`datasource.sources`** — and only those, since an unconstrained kind is refused
outright as "not an installation-selected source slot". The registry those
scopes are read from is derived per reconcile pass from the live authority
documents in the module envelope; the `MODULE_PRINCIPALS` environment variable
that once carried it is gone (#1055/#1064), so nothing here is bounded by an
environment's size.

**Two gaps belong to the module that fires triggers, not to this host, and are
named rather than worked around.**

1. **One installed trigger per release name.** This host declares every source's
   reconcile under one release name (`saas_datasource_reconcile`), because the
   name is a kind of work and the immutable reference a composition pins is
   derived from the installed manifest, which is not this host's to know. The
   firing module keys an installed trigger by that name, so a second source's
   trigger replaces the first's and per-source triggers cannot be installed for
   more than one source. Either the firing module accepts several triggers per
   release, or it admits a release named per source. Until one of those, nothing
   can consume the declaration for a deployment with more than one source — which
   is why the host's own reconcile sweep is restored as a labelled stopgap
   (MODULE.md § Unattended work).
2. **No client path to a revocable reference.** The reference this host declares
   is the source id, so revocation is not durable: a reconnect records a new
   delegation and the same reference resolves to it, re-arming an installed
   trigger with no explicit re-delegation. This host already accepts the durable
   form — `MintSourceOperationContext` takes `delegation_id` as well as
   `source_id`, and the gateway relays both. The module-authority client a firing
   module mints through offers only the `source_id` form, and then asserts that
   the answer's `source_id` equals what it sent, so a delegation id placed in the
   declaration is refused by that client rather than by this host. The fix is a
   delegation-id form on that client plus a firing module that forwards the
   declared reference to it.

**What is not proven.** No live occurrence exists anywhere. Nothing in this
repository composes a module that executes declared work, so the reference arm
is qualified at the route such a module calls and has never been exercised
against a live declared trigger; the installation-binding arm is planned and
has never been emitted at all.
