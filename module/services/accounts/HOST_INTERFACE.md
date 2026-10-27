# The host interfaces, served by accounts

accounts implements the host interfaces published by `codefly-dev/authority`:
`codefly.dev/authority@0.3.3`, `codefly.dev/composition@0.2.0`,
`codefly.dev/installation@0.2.0` and `codefly.dev/session@0.2.0`. The local
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
| The core's records: modules, tombstones, installations, role grants, delegations, epochs, owner-aware revisions, consumed nonces | migration 56 (`authority_core_*`, control-plane relations) and `pkg/infra/postgres_authority_core.go` |
| Identity, read only: organizations, memberships, teams, and the organization roles a member holds, from accounts' RBAC (role assignments and live scope grants, directly or through a team, and the membership's administrator role) | `pkg/infra/postgres_authority_core.go` (`Member`, `OrgRole`) |
| Revocation levers shared with the product: the organization's authorization revision is accounts' own, and a person's epoch includes their principal revisions | same file |
| Who the caller is: the session the perimeter verified (`signedInPerson`), or a workload credential the execution reviewer reviewed (`workload`) | `pkg/adapters/host_interface.go` |
| The signing key the core signs with, the Work Context key `Keys` publishes | `pkg/business/authority_core.go` (`ConfigureAuthorityCore`) |
| Delivery: the signed carrier and the authority document are verified and recorded by accounts' delivery owner, then composed in the core through the core's `delivered` package; every applied delivery is composed again when the core is configured | `pkg/business/authority_delivery.go`, `pkg/business/authority_core_delivery.go` |
| Audit: each change the core journals is recorded in the core's own transaction (`saas.authority.*`, `saas.module_delegation.*`); each issue is recorded before it is handed out | `pkg/business/authority_core_audit.go` |
| The hop's admission keys: leased rows judged by the database's clock, published apart from the Work Context key | migration 55, `pkg/business/hop_admission_keys.go`, `pkg/adapters/host_interface_admission.go` |
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
- **installation**: `Review`, `Install` (consent bound to the reviewed target,
  generation, revision, offer digest and a fresh operation id), `GrantRole`,
  `RevokeRole`, `ListRoleGrants`, `List`. `OfferService` (the local authority's
  setup read) is not served here.
- **session**: `SignIn` (fixture sign-in), `Current`, `ListFixturePeople`:
  accounts' own sessions.

## Proof

- The shared suite runs against accounts in
  `pkg/adapters/host_interface_conformance_test.go`, with every package's
  dependency tests, at the authority revision `go.mod` pins: accounts booted over
  its test store, its Connect listener on an ephemeral port, modules and
  solutions delivered as the CLI delivers them, and the fixture's installations
  performed through the core.
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

- No uninstall RPC exists for a host-interface installation, in either host
  (`codefly.dev/installation` declares none); the core's `Uninstall` is reached
  by host setup alone.
- The product's own installation records (`saas.accounts.v1.InstallationService`)
  are accounts' product surface and are not the core's; a fixture installation
  is performed in both.
- A narrowing of accounts' own RBAC (removing a team member, revoking a scope
  grant) runs on the control plane with an explicit organisation predicate at
  each statement, as it did under the former witness; returning the
  organisation-only ones to the tenant transaction is open.
