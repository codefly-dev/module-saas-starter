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

## The five people, everywhere

`dev-admin` is the one fixture every composition seeds — this host alone, a
module playground, a module page or a solution run locally with the authority
— and the same five people are in it everywhere:

| Sign in as | Who they are | What they hold on every delivered target |
|---|---|---|
| Sarah Chen (`admin@acme.com`) | the platform admin (`super_admin`), owner of Acme Corp | nothing of her own; she may act as any team |
| Alice Johnson (`alice@acme.com`) | the organisation admin of Acme Corp | the installer of record; she may act as any team |
| Bob Williams (`bob@acme.com`) | a member | the `<target>-viewer` team |
| Carol Davis (`carol@acme.com`) | a member | the `<target>-editor` team |
| Dana Lee (`dana@example.com`) | a member | the `<target>-installer` team |

Every role-gated target the composition delivers (a solution, a module page
declaring roles) is installed at Acme Corp by Alice, each declared role to its
`<target>-<role>` team, and Bob, Carol and Dana join the team of their role —
the fixture's `installations` row whose target is `"*"` says so once, and the
host expands it per target once every module the target reaches is composed
(a solution installed before a module it reaches arrives is held at the
narrower reach until an upgrade is approved), so a composition opts in by
seeding `dev-admin` and nothing else. A target declaring other roles gets its
teams and no member for a role it does not declare; a composing workspace's
`configurations/local/fixture-grants.yaml` may still name a target explicitly,
and that row is kept.

**To test team casting:** sign in as Alice (or Sarah), open **Admin → Teams**,
pick a `<target>-<role>` team and press **Act as this team**. The session is
now that team and nothing else — not Alice's roles, not her other teams, not
her administrator access — and the bar at the top says so; **See what this
team sees** shows the product as a member of that team sees it, and **Stop
acting as this team** restores her own session from her refresh cookie. Signed
in as Bob, Carol or Dana, the product shows that one team's reach directly.
The host alone composes no target, so on this host-only playground the row
expands to nothing and the casting walk needs a composed target (below).

## Journeys that need no solution

| Journey | Where | What proves it |
|---|---|---|
| Create an organization | manual: sign in as Sarah Chen, Platform → Organizations → Create | the new organization lists with its owner; the workspace slug follows the name (Dana Lee is a member of Acme Corp now, so the onboarding wizard of a person with no organization has no fixture person) |
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
