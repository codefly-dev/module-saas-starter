# The host interfaces, served by accounts

accounts implements the host interfaces published by `codefly-dev/authority`:
`codefly.dev/artifact@0.1.0`, `codefly.dev/authority@0.3.5`,
`codefly.dev/composition@0.2.0`, `codefly.dev/installation@0.4.0` and
`codefly.dev/session@0.2.1`, as `module/module.codefly.yaml` declares them. The
local authority implements the same five on a laptop. The shared conformance
suite (`libraries/conformance/go`) proves that the two agree, and the
declaration gate (`module-package verify-host-interfaces`, under Proof below)
holds each version named here and in the manifest to the version the interface
publishes at the authority revision `go.mod` pins.

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
| The core's records: modules, tombstones, the installations of every solution (a module's is accounts' own record), each consent's approved declaration per generation (migration 68), role grants, delegations, epochs, owner-aware revisions, consumed nonces | migration 56 (`authority_core_*`, control-plane relations), migration 68 (`authority_core_approved_modules`) and `pkg/infra/postgres_authority_core.go` |
| A module declaring no roles is installed by this host's own consent and its installation is accounts' own record (`installations`, kind `module`): an administrator's install, or a closure's, within the module envelope, retired by an uninstall or by a ceiling that no longer allows the organization. The core is configured with `HostInstallsModules`, so it installs no such module itself and reads those records live, with what each consent allows: an installation whose consent allows no capability is not active for the core, and the bindings it allows (the module's grant within the envelope, intersected with the consent, as the native mint reads it) bound every issue through them (`Installation.Bindings`). The revision the core seals to is accounts' own revision, plus the core's raise when a redelivery narrows the module (`authority_core_module_revisions`), plus the committed envelope revision, so a narrowed consent and an envelope write each end what was issued before them | `pkg/infra/postgres_authority_core.go` (`moduleInstallations`), `pkg/business/authority_core_consent.go` |
| Identity, read only: organizations, memberships, teams, and the organization roles a member holds, from accounts' RBAC (role assignments and live scope grants, directly or through a team, and the membership's administrator role) | `pkg/infra/postgres_authority_core.go` (`Member`, `OrgRole`) |
| Revocation levers shared with the product: the organization's authorization revision is accounts' own, and a person's epoch includes their principal revisions | same file |
| Who the caller is: the session the perimeter verified (`signedInPerson`), or a workload credential the execution reviewer reviewed (`workload`), bound to the organization the module envelope pins the module to | `pkg/adapters/host_interface.go` |
| The signing key the core signs with, the Work Context key `Keys` publishes | `pkg/business/authority_core.go` (`ConfigureAuthorityCore`) |
| Delivery: the signed carrier and the authority document are verified and recorded by accounts' delivery owner, then composed in the core through the core's `delivered` package; every applied delivery is composed again when the core is configured | `pkg/business/authority_delivery.go`, `pkg/business/authority_core_delivery.go` |
| Audit: each change the core journals is recorded in the core's own transaction (`saas.authority.*`, `saas.module_delegation.*`); each issue is recorded before it is handed out | `pkg/business/authority_core_audit.go` |
| The hop's admission keys: leased rows judged by the database's clock, published apart from the Work Context key; each registration proves its key once (its nonce is kept in `hop_admission_proofs`), and one key is pinned per hop instance (its pod) while its registration holds | migrations 55 and 58, `pkg/business/hop_admission_keys.go`, `pkg/adapters/host_interface_admission.go` |
| Tenancy at the perimeter: a member or an administrator of the organization a call names, before the core decides | `pkg/adapters/host_interface.go` (`requireOrgMember`, `requireOrgAdmin`) |

Who may hand this host a delivered module manifest is the `delivery` account
of the module's own namespace, by the cell's rule. A live manifest is carried
from that namespace alone. A **withdrawal** (`removed: true`) is carried from
the namespace of the module the binding last applied, **or from this host's own
module's namespace** — the same account that carries the module envelope. The
host module carries the composition: when the composition drops a module, its
namespace, delivery account and tree go with it, so nothing would be left to
carry its own withdrawal and the host would keep offering its last applied
generation. Everything else is held exactly as for any manifest — the release
signature and its domain pin, admission above the applied generation, the
ownership domain, the alias — and applying the withdrawal is the one existing
path (the target closes, its installations are revoked). Another module's
account carries neither a withdrawal nor a live manifest for a module it is
not (`pkg/business/solution_delivery.go`, `carrierNamespaces`).

The module registry the core reads is DERIVED each reconcile pass from the
applied module manifests, narrowed by the module envelope where a platform
administrator has written one (`pkg/business/module_principal_reconcile.go`).
A composed principal the envelope does not name is **withheld, not a failing
pass**: the pass applied every binding it was given, so the omission is
recorded on that binding's own row (`pending_reason`, through
`WithheldAuthorityReasons`) where an operator reads a delivery this host could
not take, and the pass returns no error for it. The host's own principal — the
module this host was composed as — needs no envelope grant: nothing mints as
the host through this registry, so its manifest's principal is entered nowhere
and leaves no row. A pass still fails for a defect in its inputs: an unreadable
manifest or envelope, a manifest that no longer verifies, a workload two
principals claim.

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
  generation, revision, offer digest and a fresh operation id; on an active
  installation a fresh approval of a newer offer is an upgrade in place, see
  [the lifecycle](#one-lifecycle-for-every-role-gated-target)), `GrantRole`,
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
  tool-metadata reader, so `tools_composed` is false and the Setup UI says agent
  tools cannot be shown because this host reads no composed tool bindings — a
  limit of the read, never a claim that none is exposed or that the installation
  is fine. The ceiling is the solution's bindings alone; a module its
  declaration depends on is carried apart as an `edge_dependencies` entry with
  its presence, and the Setup UI lists both as the modules the solution reaches.
  A role with no capability is the core's answer that no delivered module
  declares an operation requiring what it grants. A read-only delivery remains to be qualified: the
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
  on every pull request and merge-queue entry; the same suite at the pinned
  revision runs with accounts' tests in the Codefly quality phase.
- Before that suite, the same job runs the declaration gate, `module-package
  verify-host-interfaces` (in `module/tools`, with its own tests in
  `modulepackage/host_interfaces_test.go`). It reads
  `interfaces/<name>/interface.codefly.yaml` at the pinned authority commit,
  resolved in the clone the job already makes (a pin on an unmerged authority
  branch is fetched by its full id instead), and refuses: a version
  `module/module.codefly.yaml` declares above the one published there (a
  claim no suite proved); a version in another major.minor than the published
  one (passing 0.5.0's suite runs no 0.4.0 suite); an interface the pin does
  not publish; authority libraries pinned at two revisions, replaced or
  excluded (`go.mod` pins the suite, the core it decides with, the client,
  delivery and resources at one commit, and the require line must be what
  runs); and an opening sentence of this document naming another version than
  the manifest. A declared patch below the published one, within the same
  major.minor, is a notice, never a failure: the pin may move to run a newer
  patch's suite before the declaration follows it, and the declaration never
  moves first. The half that needs no network, one revision in `go.mod` and
  this document agreeing with the manifest, also runs with the `module/tools`
  tests in the module-package job.
- Last recorded run: authority `f6b111350efc` (the head of its
  installation-lifecycle branch on 2026-11-08, the revision `go.mod` pins for
  all five authority libraries), from `module/services/accounts/code`, on
  2026-11-08, with the codefly-started test store (`codefly` 0.1.238 on
  `PATH`):

      GOWORK=off go test -count=1 -timeout 30m -run '^TestAccountsPassesTheHostInterfaceConformanceSuite$' -v ./pkg/adapters

  passed 96 cases (the shared suite, the solution-principal target and the
  cases beyond the suite, as `go test -v` lists them one level under the
  test), 304 subtests at every depth below the test; failed 0; skipped 0;
  73 s (authority f6b11135). At authority 310a5954 it passes 337 subtests at
  every depth, including "a module page is installed by composition; its roles
  decide who is listed for it and who may use it" and "consent covers what a
  role reaches through the solution". The harness seeds the two owned
  solutions of the installation-resources case at the surface the suite
  redelivers (the store they bind, their roles and owned scopes), so that
  redelivery asks nothing new of their consent; a fixture module declaring
  `installation_role: none` is installed holding none. The section below
  carries no red line from this run.
- The core's store contract (`authority-core/go/storetest`) runs against this
  Postgres store in `pkg/infra/postgres_authority_core_test.go`.
- The core's own decisions are unit tested in `codefly-dev/authority`.

## Solutions are installed; every module is installed by composition

A **target** is anything delivered with a paired declaration: a module or a
solution. Only a **solution** is installed by an administrator's review:
`ListOffers` lists the solutions; `ReviewSolutionInstall` reviews one (the
core's `ReviewOffer`, with `roles_declared`, `page`, `closure`,
`optional_edges` and each role's `reach`, and the host's walk of its supporting
modules, which must name the modules the core's closure names);
`InstallReviewedSolution` installs it through the core's `Install`, carrying
the review's `reach_digest` unchanged, whose install step converges the
closure (every module installed or reused) and runs its setup job; the record
is the core's (`authority_core_installations`, with the principal the core gave
it); roles are granted with `GrantRole`, first by the install itself (below);
health and entitlement are read from that one record.

Every **module**, with or without roles, page or not, is installed by
composition in every organization: accounts' own record
(`public.installations`, kind `module`), which the core reads live
(`HostInstallsModules`). Under `HostInstallsModules` the core leaves that to
the host, so the host does it (`InstallComposedModules`): on every delivery
pass and at start (`KeepAuthorityCoreCurrent`), for every organization and
every composed module it does not hold yet, and for a new organization when it
is created. Each is made through the module install path holding no standing
authority (a declaration selecting none or nothing, a module page included),
its consent scope the envelope's, the organization's owner as owner of record
(the module install fence requires a current administrator). A module whose
declaration selects a standing role is not installed this way; the pass logs
it. What is installed already, pending or active, is never installed again;
a withdrawal revokes every installation. Supporting module administration is
a read-only view of these installations. The core
refuses a module at `ReviewOffer`, `Review` and `Install` (`NotFound`, "a module
is installed by composition"). A **module page** (a module declaring a page and
roles) holds no standing authority (`installation_role: none`; the module
install path refuses any other, `modulePageHoldsNothing`), and its roles
decide who may use it through grants on that composition installation: it
arrives with no access, and an administrator sets it up in one act
(`SetUpModulePage`, the same assignment an install makes, below), with no
install and no consent. `ListAvailableSolutions` names a module page's roles
(`AvailableSolution.roles`), so the frontend lists it beside the solutions as a
"Set up access" entry and never offers to install it. A module that gains or
loses roles keeps its installations as they are; the earlier refusals of a
delivery that made a module role-gated, and the readiness condition of a
module "installed by name", are gone with the chosen path. Entitlement
(navigation) is the core's `HeldInstallations`: a solution's healthy record,
or a module page's active composition installation, held by a grant.

**The closure is the core's.** What a review lists as required, and what the
install converges, is `decide.Closure`: every module the target reaches through
its bindings and its required edge dependencies, transitively, roles or not.
A solution reached is chosen by its own review: wherever it is reached, the
host's walker (`SolutionInstallationDependencies`) neither lists it nor walks
through it, exactly as the core does. Optional edges are shown, never
installed. `TestTheWalkersClosureIsTheCoresClosure` holds the walker to
`decide.Closure`; the conformance harness installs a solution reaching a module
page, which installs the page with the closure and a second solution reuses it.

**Consent covers reach.** The review's approval names `reach_digest`, the
digest of what each role reaches directly and transitively (`decide.Reach`);
the host carries it unchanged from `ReviewSolutionInstall` to the core's
`Install`, and the host interface's `Install` refuses a changed one
(`INSTALLATION_APPROVAL_REACH_CHANGED`). The consent receipt keeps the approved
reach (`ConsentReceipt.ReachDigest`, `Reach`, the core encoder's text) inside
the consent record (`authority_core_installations.consent`, JSON): no column
or migration, and `TestAnApprovedReachReadsBackAsTheCoresEncoderWroteIt` holds
the text to the encoder byte for byte. `upgrade_available` is the core's
`Service.UpgradeAvailable` on every read (a delivery of any module that widens
what a role reaches offers the update; holders keep the approved reach). At
start the host has the core adopt, once, the reach of a consent recorded before
reach was kept (`AdoptLegacyReach`, journalled `installation.reach_adopted`);
until then the core holds it to that same reach.

**Install assigns access.** `InstallReviewedSolution` is an administrator's
install and assigns who holds the target's declared roles, since access is
only by grant: each role goes to the team `role_teams` names for it, which
must be a team of the organization, or else to the organization's team of the
conventional name `<target>-<role>`: the team of that name (lower case,
matched case-insensitively), or the root team holding its slug as its path
(teams are unique by path, so "Target Viewer" is that team too), created
through `CreateTeam` when absent. The installing person joins the teams of the
roles named in `installer_roles` and no other, and no other team's members
change. Everything requested is checked before the core records the attempt,
and what is checked is recorded on the installation's plan
(`SolutionInstallationPlan.Access`): the teams named and the conventional
names, never a team created ahead. Nothing is created, granted or joined until
the installation is active, so an install refused or left pending leaves no
team; the call that activates it applies it: the install itself, the same
review sent again, or `RetryInstallation`. Each role's team is resolved then
(a named team since deleted falls back to the conventional one, and the answer
says so), every role is granted to its team, then the installer joins the
ticked teams. Every step is check-before-write (a member already is not added
again, a grant held is not granted or journalled again), so a resumed install
applies the recorded assignment once, never the one a replay names. A role
whose team, grant or membership fails is reported in the answer's
`Installation.access_problems` and the other roles are applied all the same:
the installation is active, and Set up grants the rest. An upgrade of an active
installation assigns nothing; its teams and grants stand. An uninstall ends the
installation's grants and keeps the teams, which are the organization's. The
fixture's installations and the host interface's `Install` assign none and
grant through the same check-before-grant (`GrantInstallationRoles`). An
install pending when this was deployed recorded the teams it created then and
applies them; one recorded before access was assigned at all activates with no
grants, and Set up grants its roles by hand. The
store-backed proofs are the `testInstallAssignsAccess` cases of the
conformance harness (`pkg/adapters/install_access_conformance_test.go`).

**Check first, then resume.** `InstallReviewedSolution` holds every supporting
module the review installs fresh to the predicates `InstallSolution` holds it
to (`checkInstallable`: the agent's release coordinates, the catalogue target,
the registry entry and its generation, the delivered declaration, the scope,
the audiences) before the core records anything: the first refusal refuses the
whole install, with no pending installation and no module installed. The store's
own fences (the administrator, the generation lock, the revision compare) still
decide in each module's write, so a convergence can still stop part-way (a
store failure, a race). The pending installation then keeps its original
review, and either the same review sent again (the original operation's
replay) or `RetryInstallation` resumes it: each module an earlier attempt
installed is installed again under the same operation, which the store replays
from its receipt (same installation, same revision), and the rest are
installed; the installation activates on the original consent. A fresh review
meanwhile reads the modules already installed as reused and may supersede the
pending attempt.

**Consent follows the release line.** The core records on each consent the
generation and offer digest it approved and keeps the declaration approved
per generation (`authority_core_approved_modules`). A redelivery whose
reviewed surface asks nothing new re-converges the consent: the installation
stays active, its approved generation advances, its revision moves, every
grant is kept (grants of a role the new declaration drops end with the
consent). A redelivery asking more leaves the installation on its consent,
active and healthy, with `upgrade_available` on the installation read; its
holders hold what the approved generation granted, and every installation
read lists those roles (`installationToWire` over the core's own rule,
`authority.ApprovedDeclaration`), until an administrator reviews the target again and installs the reviewed release on the same
installation (`InstallReviewedSolution` with a fresh review: the core's
upgrade, its step converging the closure of the new review, its grants kept).
The write that records an upgrade is fenced as an activation is, but for the
setup job: the delivered target and every supporting module the step
converged are read and locked again in that transaction and held to the
witnesses the step recorded, so a supporting module uninstalled in between
refuses the upgrade (`INSTALLATION_PREREQUISITES_CHANGED`) and the
installation stays on its consent.
`CanonicalSolutionSummaries` reads health from that record: active, its target
still delivered under the consent's principal, and the consent naming a
generation the delivered one stands on; the host compares no generation of
its own. Migration 68 fills the approved generation and digest of every
earlier consent from the host's own reviewed approval, and writes the
approved declaration only where that generation is still the delivered one
(the module's current declaration is the approved one only then). A consent
that approved an older generation reads as `upgrade_available`, and its
declaration is rebuilt at boot and on every delivery pass
(`keepApprovedDeclarations`, `pkg/business/authority_core_approved.go`): from
the delivery inbox's signed carrier and authority document for the target's
binding at that generation, verified again with the pinned keys as delivery
verifies them, from the binding's own ownership domain, and composed as a
delivery composes it. It is written only when its offer digest, as the store
answers it, is the one every consent naming that generation recorded. Where
the inbox holds no document for that generation, the documents do not
verify, or any consent recorded another digest, nothing is written and one
line is logged per installation naming it, the generation and why; the core
then refuses issuance through the installation by name until an
administrator reviews and installs the delivered release on it, and a later
pass writes what has since become available. The core holds itself to the
same: it refuses a kept declaration whose digest is not the consent's, and a
kept declaration is never replaced (the store refuses a different one at the
same generation). The store-backed proofs are
`TestRedeliveryKeepsSolutionEntitled` and
`TestMigration68FillsOnlyTheDeclarationACurrentConsentApproved` (`pkg/infra`)
and `TestADeliveryPassKeepsTheDeclarationAnOlderConsentApproved`,
`TestAnApprovedDeclarationIsRebuiltOnlyFromDocumentsThatVerify`,
`TestAnApprovedDeclarationIsWrittenOnceItsDocumentsArrive` and
`TestAnApprovedDeclarationIsWrittenOnlyWhenEveryConsentApprovedIt`
(`pkg/business`).

**Setup is per installation, not per release.** A solution's install
endpoint is called when its installation is set up: the first install, and an
explicit retry of that same pending attempt. An upgrade in place converges the
closure of the new review and does not call it again; the durable setup job
and its receiver are fenced to the pending attempt the row records, and an
upgrade leaves the row active. A release that needs its own state moved (a
schema or data migration) delivers that through its own runtime when the
release starts, never through the install step, which a release cannot count
on seeing.

Not yet: the gateway's `/_browser` path, which issues through the core, was not
exercised in a composed run for this change. The host's seal resolves a module
page's installation as any module's.

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

- Accounts product `UninstallSolution` dispatches the core's records (a
  solution's) to shared core `Uninstall`. It never removes
  supporting module installations. A supporting module's uninstall remains
  the existing Accounts lifecycle, read live by core.
- A workspace fixture row for a solution is installed as an administrator
  installs it: the row's owner reviews it and installs that review
  (`ReviewSolutionInstallAs`, `InstallReviewedSolutionAs`; the row's role is
  the installation role only where the declaration selects none), which
  converges its closure; then the owner holds the installer role and each
  declared team the row's role. A row for a module page is installed by
  composition, once, and set up the same way: the owner holds the installer
  role and each declared team the row's role on its composition installation. A pending attempt of the owner's resumes from its review while that
  review is current (the target's generation and delivery digest, and every
  supporting module's, unchanged); once a delivery moved past it, the owner's
  fresh review supersedes the attempt. An active installation is left as it
  is and only the grants it does not hold are granted, so a restart's pass
  writes and journals nothing. A row for a roleless module is installed by
  name. Rows converge in any order. A module's
  installation is recorded once, in accounts' records, which the core reads.
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

A paired declaration that selects no installation role and declares no role at
all (a manifest written before the declaration existed) leaves the host nothing
to select, so an install that names no caller role, a solution review's above
all, installs it the same way: ordinary consent, no standing grant, the review
entry saying `none` for a fresh install (a reused installation keeps what it
holds). A caller's explicit role keeps the explicit-role path. A declaration
that names roles but selects none of them is still refused: the host never
chooses among a module's roles. Migration 67 teaches the store's own check of
the current paired declaration (`lock_module_no_standing_declaration`) the same
rule, under the same delivery locks; the managed-database qualification is what
proves its ownership idiom applies as the managed principal, since a local
superuser install cannot.

The standing-role read returns empty role fields with the installation revision
for this selection. Health does not diagnose its deliberately absent grant as
missing. Team exposure must name a person/team role explicitly; installation
does not grant anyone access. Named module installation-role declarations are
currently refused as unsupported instead of mapped to a same-named organization
role. Generated declaration carriage and SQL qualification must precede deployment
of this path in a delivered host.

The canonical solution activation transaction also reconstructs the dependency
set from its locked paired declaration and the whole delivered catalogue. Binding
audiences and edge dependencies resolve by exact signed principal, and the set
closes over what each supporting module itself reaches: a solution that binds
documents installs the model documents embeds with, under the presence of the
path that reached it (a path through an optional module requires nothing). A
module's declaration is fleet-wide while a host's catalogue is a subset of it,
so a supporting module's binding onto an audience the host has not delivered is
shown absent and never refused, as the core tolerates it at issuance; a module's
required edge keeps its declared presence and refuses undelivered. A module
reaching a solution, or a target that is not a supporting module, contributes
nothing; a solution installs on its own review. A refusal names the module the
solution reached it through. The host UUID projection
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
nothing, and fresh approval may supersede pending work. Fixture convergence
does not use this adapter: its declared administrator reviews and installs the
combined review (above); it does not impersonate a non-administrator or call
`InstallAsHost` to bypass the reviewed plan. The shared host conformance suite
covers this adapter.

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
