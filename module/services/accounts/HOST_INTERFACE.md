# The host interfaces, served by accounts

accounts implements the host interfaces published by `codefly-dev/authority`:
`codefly.dev/authority@0.3.5`, `codefly.dev/composition@0.2.0`,
`codefly.dev/installation@0.3.0` and `codefly.dev/session@0.2.0`. The local
authority implements the same four on a laptop. The shared conformance suite
(`libraries/conformance/go`) proves that the two agree.

## One authority core, two thin hosts

Every decision of the authority, composition and installation interfaces is
made by the shared authority core, `libraries/authority-core/go` in
`codefly-dev/authority`. The local authority runs that core over an in-memory
store. accounts runs it over Postgres. Neither host re-implements a rule:
child issuance (the owner's current grants within the binding, never widened),
owner-selected and installation-owned scopes, owner-aware audiences, consent
bound to a reviewed offer, role grants, delegations, transfer, method admission
and every revision and epoch that revokes are the core's.

What accounts adds, and where:

| Concern | Where |
|---|---|
| The core's records: modules, tombstones, solutions' installations, role grants, delegations, epochs, owner-aware revisions, consumed nonces | migration 56 (`authority_core_*`, control-plane relations) and `pkg/infra/postgres_authority_core.go` |
| A module's installation is accounts' own record (`installations`, kind `module`): an administrator's install with consent, within the module envelope, retired by an uninstall or by a ceiling that no longer allows the organization. The core is configured with `HostInstallsModules`, so it installs no module itself and reads those records live, with what each consent allows: an installation whose consent allows no capability is not active for the core, and the bindings it allows (the module's grant within the envelope, intersected with the consent, as the native mint reads it) bound every issue through them (`Installation.Bindings`). The revision the core seals to is accounts' own revision, plus the core's raise when a redelivery narrows the module (`authority_core_module_revisions`), plus the committed envelope revision, so a narrowed consent and an envelope write each end what was issued before them | `pkg/infra/postgres_authority_core.go` (`moduleInstallations`), `pkg/business/authority_core_consent.go` |
| Identity, read only: organizations, memberships, teams, and the organization roles a member holds, from accounts' RBAC (role assignments and live scope grants, directly or through a team, and the membership's administrator role) | `pkg/infra/postgres_authority_core.go` (`Member`, `OrgRole`) |
| Revocation levers shared with the product: the organization's authorization revision is accounts' own, and a person's epoch includes their principal revisions | same file |
| Who the caller is: the session the perimeter verified (`signedInPerson`), or a workload credential the execution reviewer reviewed (`workload`), bound to the organization the module envelope pins the module to | `pkg/adapters/host_interface.go` |
| The signing key the core signs with, the Work Context key `Keys` publishes | `pkg/business/authority_core.go` (`ConfigureAuthorityCore`) |
| Delivery: the signed carrier and the authority document are verified and recorded by accounts' delivery owner, then composed in the core through the core's `delivered` package; every applied delivery is composed again when the core is configured | `pkg/business/authority_delivery.go`, `pkg/business/authority_core_delivery.go` |
| Audit: each change the core journals is recorded in the core's own transaction (`saas.authority.*`, `saas.module_delegation.*`); each issue is recorded before it is handed out | `pkg/business/authority_core_audit.go` |
| The hop's admission keys: leased rows judged by the database's clock, published apart from the Work Context key; each registration proves its key once (its nonce is kept in `hop_admission_proofs`), and one key is pinned per hop instance (its pod) while its registration holds | migrations 55 and 58, `pkg/business/hop_admission_keys.go`, `pkg/adapters/host_interface_admission.go` |
| Tenancy at the perimeter: a member or an administrator of the organization a call names, before the core decides | `pkg/adapters/host_interface.go` (`requireOrgMember`, `requireOrgAdmin`) |

## Delegation lifetimes

`Delegate.ttl_seconds` reaches the shared core unchanged in seconds. Negative
lifetimes are invalid; positive lifetimes are clamped by the core before duration
conversion (default ceiling seven days); zero explicitly means until revoked.
Accounts persists the resulting expiry in `authority_core_delegations.expires_at`
(migration 62), with SQL NULL for the until-revoked case. Delegate, GetDelegation
and RevokeDelegation project `expires_at_unix` (zero only for no expiry) and use
`domain.Delegation.Active(core.Now())`, so a stored expired grant never reads as
active. Issuance and checking remain the core's decisions; this adapter adds no
permission or lifetime fallback. Browser-edge positive-lifetime requirements
remain owned by that edge.

Expiry stops fresh issuance from the delegation; it does not revoke capabilities
already issued. Newly issued delegated capabilities are also bounded by the
remaining grant lifetime at owner revision 7954; previously issued tokens retain
their signed expiry and verifier clock tolerance. The selected owner correction
f82a98e093ef uses one issuance instant for a finite grant's remaining lifetime
and root timestamps, so revision/seal-read latency cannot extend the signed
window. Verification, child derivation and post-sign grant checks remain live;
Accounts adds no deadline arithmetic. Parent exchange remains bounded by
the parent's remaining lifetime and scopes. Explicit revocation is the separate
operation that invalidates existing descendants through authority revisions.

Apply migration 62 before deploying this code, and keep expiry-bearing traffic
off old Accounts peers that ignore the stored timestamp. The down migration
discards expiry data: rolling back to indefinite delegation behavior is not a
behavior-preserving rollback.

## The interfaces, method by method

- **authority**: `Issue` for a person (the signed-in session), a workload, an
  installation acting for itself, a parent token (a module's hop through its
  binding), or a delegation; `Check`, with a named method held to the audience's
  delivered method policy (advisory answers the refusal beside the context);
  `ListResources`; `Delegate`, `RevokeDelegation`, `GetDelegation`;
  `AttachResource`, `DetachResource`, `AuthorizeTransfer`; `Keys`;
  `RegisterAdmissionKey`.
- **composition**: `ApplyManifest`. A delivery the core would refuse to compose
  (for example a solution that declares no installer role) is refused before it
  is recorded.
- **installation**: `Current` (a module reads its own active installation in
  the tenant of a current parent addressed to it, proving itself by its
  workload credential), `Review`, `Install` (consent bound to the reviewed target,
  generation, revision, offer digest and a fresh operation id), `GrantRole`,
  `RevokeRole`, `ListRoleGrants`, `List`, `ListHeld` (a member's own
  navigation: each active installation of their organisation they hold a role
  on, granted to them or to a team of theirs, with those roles — the core reads
  their teams from the store, so leaving one drops an entry at once; holding
  nothing is an empty list, and a caller acting at another organisation is
  refused naming it). `OfferService`, the setup read beside
  it: `ListOffers` (an administrator's) and `ReviewOffer`, answered from the
  core's `ReviewOffer` with no floor of this host's own, so whoever the core
  answers (an administrator with the consent tuple, a member holding a role on
  the active installation without it) is answered here, and every refusal is the
  core's. The authority-owned `offers.Project` projects roles → operations →
  tools and the permission ceiling; `pkg/adapters/host_offers.go` only adapts
  its result to this host's wire messages, including installation role names from
  that same reviewed snapshot. Redelivery during conversion cannot replace them
  with a newer declaration. The local authority uses the same
  projection (authority #47 and #48, selected at `0ef857ee3ba5`), including
  Core's default tool exposure when no explicit metadata is declared. Accounts has no production
  tool-metadata reader, so `tools_composed` is false and the Setup UI reports
  exposure as unavailable. A read-only delivery remains to be qualified: the
  CLI treats a `runnable-bindings` consumer as an executor, including network
  reach; the Setup read must not create execution authority. The
  solution's side, `InstallableService`, is not served here.
- **artifact** (`host.artifact.v1`): `Approve`, `Authorize`, `Revoke`. A module
  proves itself by its workload credential and presents a parent capability the
  core issued, addressed to it, and the core decides
  (`authority.Service.DecideArtifact` over `decide.Artifact`), so this host and
  the local authority answer alike: a policy is a resource kind the module
  serves; approving and revoking need the person present holding that kind's
  `activate` action and a run its `run` action, on the whole kind, which is the
  whole requirement — no administrator tier; the installation is the module's
  own active one at the parent's organisation, a foreign and a missing one
  answering alike; an artifact's source is that installation's namespace, as
  `InstallationService.Current` names it; the digest is `sha256` of the
  identity's deterministic protobuf encoding, and every answer, `Revoke`
  included, carries the revision the artifact was approved at. accounts adds
  the records alone (`pkg/adapters/host_interface_artifact.go` over
  `business.ApproveHostArtifact` and its siblings, which keep no rule). The
  SaaS-only `ModuleCapabilitiesService` artifact path is a different face, with
  its own installed-policy semantics, and is not this decision. Consent is
  stored per organization, installation, module, policy and content digest
  (migration 61). The same content can have independent approvals under two
  policies; neither approval authorizes or revokes the other. An omitted
  artifact identity is rejected by the core as malformed.
- **session**: `SignIn` (fixture sign-in), `Current`, `ListFixturePeople`:
  accounts' own sessions. Fixture listing names the selected organization from
  its current stored record through the same scoped read as sign-in. It opens
  no session; disabled fixture authentication reads and lists nobody, and an
  organization read failure refuses the listing rather than returning a partial
  list or an empty display name.

## Proof

- The shared suite runs against accounts in
  `pkg/adapters/host_interface_conformance_test.go`, with every package's
  dependency tests, at the authority revision `go.mod` pins: accounts booted over
  its test store, its Connect listener on an ephemeral port, modules and
  solutions delivered as the CLI delivers them, and the fixture's installations
  performed through the core.
- Every module the harness or the suite delivers is installed at both
  fixture organizations by their administrators through accounts' own install
  path (`Fixture.InstallModule`); `conformance-idle` stays installed nowhere.
  The harness proves a module workload is issued nothing where it is not
  installed, nothing past the organization the envelope pins it to, and
  nothing after an administrator uninstalls it, when what it was issued fails
  its next check.
- The solution-principal target (`RunSolutionPrincipalTarget`) runs in the same
  harness. A person leaving a team or the organization goes through accounts'
  own production paths (`RemoveTeamMember`, `RemoveOrgMember`): the narrowing
  applies at once, the next issue no longer carries what the membership gave,
  and the removal is in the audit journal naming the person removed.
- Installation conformance changes the administrator's live role through the
  normal membership upsert, retaining the session and membership. The shared
  suite checks revocation after review and while acknowledgement is paused;
  restoring the role permits the unchanged pending approval to resume.
  A member without that role receives `PermissionDenied` with a reason naming
  the required organization administrator or owner role.
- `.github/workflows/ci.yml` (`host-interface-conformance`, the required
  context "Host interface conformance (authority main)" and a release gate)
  runs the suite at authority main, whenever main contains the pinned revision,
  on every pull request and merge-queue entry.
- The core's store contract (`authority-core/go/storetest`) runs against this
  Postgres store in `pkg/infra/postgres_authority_core_test.go`.
- The core's own decisions are unit tested in `codefly-dev/authority`.

## Not yet

The solution entitlement projection now reads the shared core's
`HeldInstallations` decision. It pairs each active, explicitly held installation
with the applied host target only when the signed principal and generation
agree. The catalogue contributes identity, not permission: an installation's
machine role and accountable owner do not implicitly grant a person access.
Modules retain their existing scope-root admission. A canonical solution carries
its shared installation ID and no invented legacy scope-node ID.

Canonical solution setup uses the existing installation-step outbox. The host
keeps its reviewed release/target pairing and prerequisite witnesses beside the
canonical consent receipt; it writes no second solution installation. Enqueue
and the receipt's job link commit together. A lease-bound setup credential is
issued through the shared owner implementation and carries only installation
setup authority. The receiver body is exactly `installation_id`,
`organization_id`, and `revision`, without scope grants or persisted tokens.

A successful queue job does not activate the installation. Core's final
serializable write checks the same receipt and revision, the exact succeeded
job, and locked module consent and durable delivery rows before writing active.
Failure stays pending. An explicit retry preserves the original requested role,
owner, approval and operation identity; core rechecks them before a dead-letter
job may be replayed. A different administrator reviews afresh rather than
impersonating the original approver. Uninstall calls core and leaves supporting
module installations intact.

The host's installation listings and point reads project canonical solution
records alongside existing module records. Canonical solutions have no legacy
scope root or team-exposure grants; their person/team roles remain the shared
installation service's. The database encoder retains the owner's explicit
installation selector and dependency declarations verbatim. Ordinary issuance
and gateway entitlement checks continue to refuse pending installations.

Source tests are not deployment qualification. The complete review form,
dependency convergence, generated signed-carrier fields, SQL activation barrier
and actual queue-to-receiver journey must be qualified together before delivery.

- Accounts product `UninstallSolution` dispatches canonical solution records to
  shared core `Uninstall`. It never removes supporting module installations.
  Module uninstall remains the existing Accounts lifecycle, read live by core.
- Existing fixture setup still records a solution twice: by the product's own records
  (`saas.accounts.v1.InstallationService`) and by the core; a fixture
  installation is performed in both. A module's is recorded once, in
  accounts' records, which the core reads.
- The hop's per-instance pin and its registration proofs are kept in
  Postgres, shared across replicas; the instance is the pod the reviewed
  credential names.
- A narrowing of accounts' own RBAC (removing a team member, revoking a scope
  grant) runs on the control plane with an explicit organisation predicate at
  each statement, as it did under the former witness; returning the
  organisation-only ones to the tenant transaction is open.

### Durable client metadata at Authority 7954

Accounts persists the complete client contract introduced at owner revision
7954a627c818 and selects its deadline correction f82a98e093ef. It preserves every
`Client` record field and client delegation field in the existing control-plane store
(migration 63). A disabled client still reads as its stored record; core owns the
refusal. Missing records wrap the owner not-found error, while SQL failures remain
errors. Client replacement and delegation writes share the existing transaction
and rollback rules. This adds no public registration or OAuth endpoint.

This owner revision bounds newly issued module-delegated capabilities by the
remaining delegation lifetime. Previously issued tokens retain their signed
expiry and verifier tolerance; the earlier 39369 lifetime evidence applies only
to that revision. Accounts adds no local token-expiry enforcement. Migration 63
must precede this adapter, and older adapters must not handle client-bearing
state. Downgrading migration 63 discards client metadata and is not a safe
behavior-preserving rollback after clients have been used.
## Declared module machine authority

A paired module declaration selecting `installation_role: none` records ordinary
module installation consent and lifecycle without an agent standing grant. The
host does not create an empty role or choose an organization role. The durable
`no_standing_authority` consent marker is immutable; the store checks the current
paired declaration under delivery locks before accepting it. A caller-supplied
role is refused. Existing explicit-role module installations retain their path.

The standing-role read returns empty role fields with the installation revision
for this selection. Health does not diagnose its deliberately absent grant as
missing. Team exposure must name a person/team role explicitly; installation
does not grant anyone access. Named module installation-role declarations are
currently refused as unsupported instead of mapped to a same-named organization
role. Generated declaration carriage and SQL qualification must precede deployment
of this path in a delivered host.

The canonical solution activation transaction also reconstructs the dependency
set from its locked paired declaration and referenced modules. Binding audiences
and edge dependencies resolve by exact signed principal. The host UUID projection
is compared separately; aliases, binding keys and release strings are not identity
fallbacks. Omitted required modules, duplicate witnesses, unrelated additions or
changed generations refuse activation. Ordered edge declarations are retained;
presentation sorting never alters them. Per-module receipt operation, intent and
revision are retained beside scope, standing selection and delivery witnesses.
The combined review checks delivered incoming access compatibility and converges
separate module consent before setup. The final transaction reconstructs the
required union again; a callback-only check cannot fence concurrent revocation.

### Combined reviewed installation API

An organization administrator calls `ReviewSolutionInstall` with the catalogue
target UUID, then sends its unchanged `ReviewedSolutionInstall` to
`InstallReviewedSolution`. The review resolves signed principals through the
existing catalogue to host targets/bindings, pairs exact releases and delivery
digests, and retains the shared solution approval plus individual module
approvals. The host derives identity and declared machine authority; it never
selects an organization administrator role. Optional presence edges create no
consent. A required module without a supported authority declaration refuses
by principal. Existing sufficient module consent is reused without revision or
receipt changes. Named module selector support remains explicitly unavailable.

Incoming invoke, lookup and headless compatibility is checked separately through
shared `decide.ScopesFor`, `decide.Served` and `scope.Attenuates`, against the exact
reviewed module. Empty requirements add nothing. Module consent separately gates
active installation and its outbound bindings; target compatibility does not
grant a person or machine access. The final transaction reconstructs and checks
the same graph and locks the durable prerequisite witnesses.

A pending canonical record retains the original review before dependency work.
Retry resubmits that review or uses `RetryInstallation`; current-person
authorization remains mandatory. A setup ACK after the request's bounded wait
is exposed as awaiting continuation, never activated by a queue worker. Fresh
reads expose queued, running, retrying, refused, canceled and acknowledged setup
outcomes from only the exact linked job. Only explicitly public bounded receiver
refusals are shown; transport diagnostics stay private.

Legacy product solution writes refuse on a configured canonical host. Legacy
records remain visible as requiring fresh canonical review; their human grants
are not copied. Existing module installation and revocation records retain their
owner path. Source tests and generated routes are not queue/receiver, SQL or
deployment qualification; see the delivery receipt for actual executed gates.


The REST entry points are `POST /v1/solution-installations/review` with
`{"orgId":"<organization UUID>","targetId":"<catalogue target UUID>"}`, then
`POST /v1/solution-installations` with
`{"orgId":"<same organization UUID>","review":<the complete returned review>}`.
These use the current authenticated administrator. Preserve the returned review
for retries; regenerate it only when intentionally requesting new consent. A new
valid approval can supersede a pending operation with a new canonical revision;
an old acknowledgement cannot activate that revision. An active replay does not
enqueue setup or renew supporting module consent.

Use the existing `OfferService.ReviewOffer` projection to present the solution's
roles, operations and permission ceiling. Match its approval target, generation,
revision and digest to the combined review before presenting consent; an
unmatched projection requires a fresh review. The module consent `scope` is the
existing owner's serialized input, not a replacement UI permission language.
Person/team role assignment remains a separate explicit operation.

The native SQL gate `TestCanonicalSolutionActivationFencesAcknowledgedPrerequisites`
checks the final transaction after ACK against module revoke/redelivery and
solution revoke, plus durable failure/retry/cancellation and wrong-attempt read
projection. `TestCanonicalCombinedInstallQueueThroughRuntime` additionally takes
`INSTALL_RECEIVER_BINARY`, built from the maintained solution runtime, and drives
the combined review/install through real SQL, private HTTP claim/finish, Accounts
Check and the runtime's `Serve` receiver. It covers refusal/recovery, retry,
four separately consented supporting modules, late ACK awaiting current-person continuation, active replay, postactivation
setup refusal and shared-module preservation. Its HTTP sender uses the gateway
wire contract; deployed gateway/browser verification is separate.

The existing shared single-target `Install` entry uses the same durable plan,
queue and activation fence. It preserves the original caller, organization,
role, owner and full approval, including empty inputs. It may reuse supporting
module consent but cannot approve a new module; that requires the combined
review. An absent solution selector retains the shared API's explicitly supplied
legacy role. Changed inputs on the same operation refuse, active retries enqueue
nothing, and fresh approval may supersede pending work. Fixture convergence uses
its explicitly declared administrator to review and install for the intended
owner; it does not impersonate a non-administrator or call `InstallAsHost` to
bypass the reviewed plan. The shared host conformance suite covers this adapter.

`TestIntegration_Gateway_CombinedInstallationAuthentication` executes real login,
key verification, gateway routing and Accounts handlers for both REST and Connect
entry points: missing/invalid sessions refuse, members cannot install, and an
administrator reaches the reviewed-target/actor checks. This is local gateway
qualification; deployed browser confirmation remains separate. The ordinary
combined review UI remains a required consumer of the API above. The API docs
viewer is not a qualified manual route until its served specification and
current-session transport are available.

A combined review reads the supporting module installation history, including
revoked rows, and binds its latest revision into a fresh module approval. The
existing module installer alone creates the replacement installation and agent;
it never revives a revoked principal. A concurrent module lifecycle change
invalidates the review. Repeating the original successful request preserves its
receipt, and a newly delivered release still requires its own current review.
