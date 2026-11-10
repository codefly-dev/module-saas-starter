# Host playground

The host's playground is the host itself, composed and running with its own
fixture, so a person can try what it does without a solution. It is a
composed service started the normal way, with the real UI; there is no second
server.

```bash
codefly run service --fixture dev-admin          # from the repository root
codefly endpoint saas-starter/frontend --require-up   # the address, never typed
scripts/playground-check.sh                      # the journeys marked as checked, asserted
```

Use the CLI release in `scripts/ci/install-codefly.sh` or newer: an older build
fails at boot with `no configuration found for egress`. Add `--naming-scope
<name>` to every command to keep a run disjoint from another. Sign in from the
picker as one of the fixture people; accept the terms each person is shown.

## Journeys that need no solution

| Journey | Where | What proves it |
|---|---|---|
| Create an organization | manual: sign in as Dana Lee (no organization yet), onboarding | she becomes its owner; the workspace slug follows the name |
| Create a team | manual: Admin, Teams, as Sarah Chen | the team lists |
| Grant a person a role explicitly, access follows | checked: `playground-check.sh` | Carol reads the example collection after the grant and not after it is revoked |
| Refusal names its reason | manual: Bob Williams opens Admin | "You don't have access to Admin", with his role and who can change it |
| An administrator gets nothing for free | checked: `playground-check.sh` | organization admins (Alice, Alex) and a platform `super_admin` with her own grant removed read nothing; a platform role reads no other organization's audit trail |

## The tenth journey: an administrator with no grant (runs in the conformance harness)

An organization owner or admin holding no grant is refused at a module that is
neither role-gated nor owner-aware, and issued the scope once granted; revoking the
grant withdraws it. The role "admin" names who administers and holds no scope. This
is the path the old wildcard served (the store answering `*:*`, and the Work Context
mint's `role IN ('owner','admin')` arm).

It does **not** run in this host-only playground: the playground composes no module,
so it has no audience to mint at (`Issue` answers `audience "records": not found`).
It runs where modules are composed: `TestAccountsPassesTheHostInterfaceConformanceSuite`,
subtest "an administrator with no grant is issued nothing without one"
(`administrator_without_grant_test.go`), which boots the real accounts and Postgres
with the suite's composed modules (`records` checks no owner and is not role-gated, so
a refusal there is not an owner-aware module refusing anyway). Verified failing on the
pre-#1328 production code ("An error is expected but got nil": the administrator was
issued the scope) and passing now. Closing the gap for a person-run playground needs a
composed module delivering a declaration; that is the composition work, not the host's.

## Not here

Installing a solution and the role teams it creates need a real solution, and
this repository never names one. That journey runs from a solution's own
workspace, composed onto this host (see the Codefly guide's "Run a solution").

Every journey runs as a plain person holding only explicit grants; the few steps that must run as an administrator (granting, revoking) say why in the script.
