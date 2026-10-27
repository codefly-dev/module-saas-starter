# The host interfaces, served by accounts

accounts implements the host interfaces published by `codefly-dev/authority`:
`codefly.dev/authority@0.3.4`, `codefly.dev/composition@0.2.0`,
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
  accounts' own sessions.

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
- `.github/workflows/ci.yml` (`host-interface-conformance`, the required
  context "Host interface conformance (authority main)" and a release gate)
  runs the suite at authority main, whenever main contains the pinned revision,
  on every pull request and merge-queue entry.
- The core's store contract (`authority-core/go/storetest`) runs against this
  Postgres store in `pkg/infra/postgres_authority_core_test.go`.
- The core's own decisions are unit tested in `codefly-dev/authority`.

## Not yet

- No uninstall RPC exists for a solution's host-interface installation, in
  either host (`codefly.dev/installation` declares none); the core's
  `Uninstall` is reached by host setup alone. A module's installation is
  uninstalled through accounts' own product surface, and the core sees it at
  once.
- A solution's installation is recorded twice: by the product's own records
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
