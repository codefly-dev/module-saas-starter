# AGENTS.md

Orientation for anyone — human or agent — working in this repository. It links to
the authoritative docs rather than restating them; when this file and a linked doc
disagree, the linked doc wins. Area depth lives in nested `AGENTS.md` files
(closest file to the one you are editing wins, like `.gitignore`); procedures live
in `.claude/skills/`. See [Where the depth lives](#where-the-depth-lives).

## What this repo is

`saas-starter` is a Codefly **module**: a multi-tenant SaaS backend
(three-layer authorization, Postgres RLS, RBAC, impersonation, audit), an
authenticated Next.js product, a separately deployable marketing site, and the
supporting cache/store/vault/telemetry services. It is published as an
immutable module package that downstream workspaces **compose** (never fork).

- Architecture, service graph, and capability ownership: [MODULE.md](./MODULE.md)
- Feature inventory: [module/FEATURES.md](./module/FEATURES.md)
- First-run walkthrough: [module/GETTING_STARTED.md](./module/GETTING_STARTED.md)

`codefly run service --fixture dev-admin` boots the whole graph locally (Docker
must be running; `codefly doctor` checks prerequisites, `codefly clear` reaps
strays). The provider stack, driving a solution against this host, and diagnosing
a run that will not come up are in the `run-the-starter-locally` skill.

## How to work here

**A gap in the tooling is a bug in the tooling** — never a reason to reach around
it. Not as a "workaround", not "just this once", not "until the verb lands". If
`codefly` cannot express what you need, the deliverable is a fix or a precise
issue against the CLI, not a local substitute for it.

**Never hack. Always provide the best fix, even when it spans repos.** The right
fix living in someone else's repo is not a reason to work around it in yours —
open the pull request there. If it genuinely cannot be fixed now, the deliverable
is a precise issue against the owner *plus* an explicitly labelled stopgap, never
an unlabelled one.

**Classify every change that makes something work**, in the PR body: a *fix* at
the place that owns the behaviour, or a *hack*. A hack does not become a fix by
working, by being small, by being local, or by the real fix belonging elsewhere.

**Never hardcode what the system resolves** — injected environment, derived ports,
service addresses, credentials copied out of another component's config. If you
are typing one, you are encoding something true only on your machine for the next
ten minutes, and a service started that way can boot, serve, and still never
register, silently.

**Diagnose, do not pattern-match.** "It started working when I set X" is not a
diagnosis: set X back and confirm it breaks. Do not trust an error message before
checking it — a runtime here has reported a secret that "does not match its
digest" when the cause was a missing internal token and the digest was correct.

**Say what you did not verify.** Unverified is not working. If you could not
exercise something — a suite needing Docker, a graph you could not boot, a release
path you could not run — the PR body says so.

## Naming and confidentiality

Everything in this repository — issues, PRs, docs, specs, code, comments, tests,
fixtures, commit messages — must use **only generic placeholder names** for any
company, product, person, team, or dataset: `Acme`, `ExampleCorp`, `Example
Solution`, `Jane Doe`, `user@example.com`, and the like. Never write the name of
a real customer, partner, employer, or downstream consumer of this module, and
never describe the module as a dependency of any specific named product.

This is a hard boundary, not a style preference. This module sits **below** its
consumers: they depend on it, never the reverse, so it carries no build-time or
documentation-level knowledge of who consumes it. Describe a consumer generically —
"a consuming solution", "the downstream product". Capabilities belong here in their
**generic** form (RBAC, delegation, Work Contexts, audit) so every consumer reuses
them; consumer-specific wiring stays in the consumer's own repo.

The rule holds for public **and** private files alike, and for records as much as
for files: a pull request's title, body and commit messages are checked before it
can merge. Only the local hook prevents publication — by the time CI runs the
commit is already pushed to a public repository, so CI blocks the **merge**, and a
published record cannot be retracted afterwards. Run `node
module/tools/naming-gate.mjs check` over the tree; the record half, the local
hook, and how to recover from a failure are in the `land-a-pull-request` skill.

## Boundaries — non-negotiable

This repo is a **module** — the **host**, the root every other module composes
into. Its whole responsibility is one sentence: the first paragraph of its
handbook page, `modules/saas-starter.md` in the handbook of the workspace that
composes this host. Read that page before changing anything here. If the change
would make that sentence need another clause, the change does not belong in this
repo.

- **Owns:** identity, tenancy, permissions, jobs, audit, approvals,
  notifications, data sources, and the product surfaces.
- **Never contains:** domain content of any kind — documents, rows, models,
  agents and deals are the other modules'; a flag never grants an entitlement,
  raises a quota, or replaces authorisation.
- **Depends on:** nothing from other modules. It is the root of the composition.

The rules, governed by the handbook's `concepts/boundaries.md`:

1. **Dependencies go one way**: solution → module → service, with the host beneath
   every module. Never import, read the tables of, or hard-code the internals of
   another module. Cross a module boundary only through the four seam contracts
   (Ref, journal, host ports, provenance).
2. **Nothing here names what sits above it.** A service never names a module, a
   tenant or a permission; a module never names a solution, a customer or a
   business line. If the task needs that word, stop — the thing belongs above,
   through an extension point.
3. **One consumer's need is not a feature of this repo.** Ship it in the consumer
   through the extension point; if no extension point exists, open a handbook
   track. No special case here, however small, however temporary.
4. **The boundary test in CI is part of the definition of done.** Never weaken,
   skip, or allowlist past it to land a change.
5. **If you cannot finish without breaking one of these, stop and say so.**
   Half-done inside the boundary beats done across it.

The test is `scripts/check-boundaries.sh` (denylist and baseline beside it), and it
is additive to the naming gate, the SDK-boundary job and the boundary tests already
here. Its baseline shrinks and never grows: clean a file, delete its line.

## Repository layout

- `module/` — the canonical module source that ships to consumers. **This is
  the tree the base-integrity tooling and CI run against.** See
  [module/AGENTS.md](./module/AGENTS.md) before editing anything under it.
- `modules/saas-starter` — a symlink to `module/` (the workspace-composed view
  Codefly expects under `modules/<name>/`).
- `main.go` / `gitops.go` — this repo is itself the `saas-starter` **module agent**
  binary; composing the module runs this code to regenerate the per-service
  manifests. `agent.codefly.yaml` carries its name, publisher and version.

## Where the depth lives

A **solution** or composed module is independently deployed and **self-registers
with this host at runtime** — a Module-Federation remote in the frontend, an upstream
in the gateway — so the host renders and proxies it with no rebuild. Nothing here
names a specific one. The depth sits with the code that owns each half:

| File | Covers |
| --- | --- |
| [module/AGENTS.md](./module/AGENTS.md) | the shipped tree: generated vs authored files, the six Go modules and how to test each, configuration groups |
| [module/services/auth-gateway/AGENTS.md](./module/services/auth-gateway/AGENTS.md) | upstream registration, the composed-module REST prefix, the credentials both take |
| [module/services/accounts/AGENTS.md](./module/services/accounts/AGENTS.md) | the durable registry record, module principals, minting a module Work Context, mesh reachability |
| [module/services/frontend/AGENTS.md](./module/services/frontend/AGENTS.md) | the registration route, public vs internal projections, remote loading and CSP, the published kit |

Skills in `.claude/skills/`, loaded when the task calls for them:
`run-the-starter-locally`, `land-a-pull-request`, `refresh-base-manifest`,
`pin-a-service-agent`, `cut-a-release`.

## Building, testing, and CI

- Canonical **service** gate: `codefly ci run`. It owns lint, compile/typecheck,
  tests, dependency/vuln audit, SBOM, and container build for every service in
  the graph. See [RELEASE_GATES.md](./RELEASE_GATES.md).
- Beside it, CI runs nine repository-specific gates that no service owns — base-file
  integrity, authorization coverage, the release-gate contract, interface docs and
  story tests, the kit's version, provider shims, marketing isolation, the SDK
  boundary, the immutable module package. [RELEASE_GATES.md § Repository-specific
  gates](./RELEASE_GATES.md#repository-specific-gates) says what each runs, and
  `release-gates.test.mjs` holds this prose to the enforced set.
- More checks run as *steps* inside the `base-integrity` job than as gates of their
  own — tenant RLS coverage, migration pairing, pinned plugin versions on generated
  Go, the naming gate over both tree and records, commit identity. Never precede
  such a list with a count: nothing enforces one, so two changes that each add a
  step and each bump the same number merge cleanly into a total that is silently
  wrong.
- Editing any base file means refreshing `module/tools/base-manifest.json` — the
  easiest gate here to trip (`refresh-base-manifest` skill). Go suites live in six
  independent Go modules with no `go.work`
  ([module/AGENTS.md](./module/AGENTS.md#go-suites)).
- Vulnerability policy: the complete audit runs non-blocking so vendor-image
  findings stay in the evidence report, while a separate fail-closed step enforces
  first-party services and production frontend dependencies ([RELEASE_GATES.md §
  Vulnerability
  policy](./RELEASE_GATES.md#vulnerability-policy-and-its-one-exemption)).
- `main` merges through a **GitHub merge queue**, not by pressing Merge. A
  successful `gh pr merge` proves neither queue membership nor merge, and every
  required check must report on `merge_group` or it stalls the whole queue
  (`land-a-pull-request` skill).

## Cutting a release

Two tag tracks share this repository on separate version axes: the **`v0.0.N`
deploy counter** consumers adopt via `codefly sync module`, and the **immutable
module package** (`module-package/vX.Y.Z`, from
`module/module.package.codefly.yaml`) — only the second triggers the
module-package publication job. Cutting a tag here does **not** publish a Go or
Python client; the `saas-sdk-go` and `saas-sdk-python` repositories distribute
those under their own tags. It *does* publish the TypeScript client:
`@codefly-dev/saas-sdk` goes to GitHub Packages on every deploy-counter tag, so a
contract change reaching that tree needs a `version:` bump in its `package.json`
or the release fails. Recipes and traps: the `cut-a-release` skill.

## Doc index

Start from [MODULE.md](./MODULE.md), whose "Quick links" indexes the full set, and
[RELEASE_GATES.md](./RELEASE_GATES.md) for the gates. Which claim in those
documents is backed by what, and which are kept only as history, is registered in
[CLAIM_INVENTORY.md](./CLAIM_INVENTORY.md).
