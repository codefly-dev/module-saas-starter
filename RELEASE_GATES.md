# Release gates

The SaaS Starter has one canonical release gate: `codefly ci run`.

GitHub Actions is only a provider adapter. It checks out the repository,
installs the pinned Codefly CLI, supplies the base and head revisions, and
invokes the gate. It does not encode Go, Rust, Next.js, protobuf, dependency,
container, or service-specific commands.

For version tags, successful completion of this gate — together with every other
mandatory check, through the aggregate described in [Publication
gating](#publication-gating) — unlocks the immutable module-package publication
job. That job handles only the release transport: strict package-manifest
validation, deterministic archive construction, digest, aggregate SBOM,
provenance signing, and immutable GitHub Release publication. It does not
duplicate service build or test policy.

Publication requires three release-only repository secrets: the read-only
Administration token `RELEASE_ADMIN_TOKEN` for immutable-release policy checks,
the base64 Ed25519 key `RELEASE_PROVENANCE_PRIVATE_KEY` for Core's detached
module provenance signature, and its independently configured trust-policy key
`RELEASE_PROVENANCE_PUBLIC_KEY`. Missing, malformed, or mismatched credentials
fail before a release is created.

## Publication gating

No job that writes an artifact runs unless every mandatory check has *actually
succeeded*. That is enforced by a single aggregate job, `release-gates`, which
is the only entry in every publisher's `needs`:

```
base-integrity ───────┐
authz-coverage ───────┤
release-contract ─────┤
docs-sync ────────────┤
kit-version ──────────┤
provider-shim ────────┤                     ┌─▶ publish-module-package  (module-package/v*)
marketing ────────────┼─▶ release-gates ────┼─▶ publish-frontend-kit    (v*)
sdk-boundary ─────────┤   (!cancelled() +   └─▶ handbook-surface-bump   (v*)
module-package ───────┤     decide)
codefly-plan ─────────┤
codefly-quality ──────┤
codefly-supply-chain ─┤
codefly-build ────────┘
```

Before this aggregate existed, `authz-coverage` was an independent job that no
publisher listed in `needs`, directly or transitively. A tag run could therefore
publish while the authorization gate was red, and a red overall workflow does not
retract an artifact that is already public. Branch protection on pull requests
narrows the opportunity but creates no tag-time dependency.

`release-gates` runs with `!cancelled()`, so a failed dependency does not skip it
— it reaches its own step, which calls `scripts/ci/release-gates.mjs decide` with
`toJSON(needs)`. A bare `needs:` list cannot tell a check that passed from one
that never ran; `decide` fails the aggregate unless every mandatory gate reports
`success`, so `failure`, `cancelled`, and an unexpected `skipped` all block
publication identically. The publishers carry no `always()` of their own, so a
failed aggregate skips them. (`always()` would work too, and the contract accepts
either; `!cancelled()` additionally lets a run superseded by `cancel-in-progress`
skip the aggregate rather than record a failure nobody should read.)

`decide` also refuses a release ref whose plan was **delta-scoped**. `codefly-plan`
forces `--all` from the ref, not from the push payload: `github.event.before` is
the zero sha only for a *new* tag, so force-moving an existing tag would otherwise
scope the mandatory gates to a delta and publish services that were never rebuilt
or re-audited for that release. The aggregate re-checks `codefly-plan`'s `all`
output on every release ref, so a scoped release fails even with all thirteen gates
green.

### Mandatory gates per tag track

Both tracks publish from the same tree, so both require the same gates. There is
no per-track exemption.

| Gate | `v*` (deploy counter) | `module-package/v*` | Note |
| --- | --- | --- | --- |
| `base-integrity` | required | required | canonical manifest freshness |
| `authz-coverage` | required | required | RBAC, audit, and no-broadening |
| `release-contract` | required | required | this gating graph itself |
| `docs-sync` | required | required | interface docs and story tests |
| `kit-version` | required | required | published frontend kit version vs. content |
| `provider-shim` | required | required | non-writing provider shims |
| `marketing` | required | required | marketing isolation build |
| `sdk-boundary` | required | required | Codefly SDK boundary and contracts |
| `module-package` | required | required | package contract, determinism, buf breaking |
| `codefly-plan` | required | required | affected-service resolution |
| `codefly-quality` | required | required | affected-scoped (see below) |
| `codefly-supply-chain` | required | required | affected-scoped (see below) |
| `codefly-build` | required | required | affected-scoped (see below) |

The three affected-scoped Codefly jobs carry the **one** deliberate exemption:
off a release tag, a plan that explicitly reports no affected service
(`has_work=false`) legitimately skips them, and `decide` accepts that skip so a
docs-only pull request is a near-instant no-op. On either release tag the
exemption does not apply — those jobs force themselves to run there via their own
`if:`, so a skip is a defect, not a scoping decision. The exemption also requires
`codefly-plan` itself to have succeeded **and** to have said `false` in so many
words: an absent or unrecognized `has_work` (a renamed output, a lost
`GITHUB_OUTPUT` write) exempts nothing, so a broken plan job cannot silently
retire the three heaviest gates while this one stays green.

`handbook-surface-bump` counts as a publisher alongside the two package jobs: it
dispatches the release to a downstream repository, an outward-facing write with
the same "cannot be retracted" property.

Its dispatch is not, however, a publication of the release itself: every artifact
is already public by the time it runs, and nothing is retracted when it does not
happen. `scripts/ci/announce-release.mjs` splits the two cases that follow from
that. Missing either the `HANDBOOK_REPOSITORY` variable or the
`HANDBOOK_DISPATCH_TOKEN` secret, it records a `::warning::` and succeeds without
dispatching — an unconfigured docs announcement must not mark a correct release
red beside a real publication failure. Provisioning is two operator commands, so
a half-configured repository warns on the same footing rather than failing in the
window between them; the annotation repeats on every release until both are set.
With both present, a dispatch the handbook rejects still fails the job.
`node --test scripts/ci/announce-release.test.mjs` covers all three outcomes —
including that an unconfigured run dispatches nothing at all — and runs in
`release-contract`.

### The contract test

`release-contract` runs `scripts/ci/release-gates.mjs check`, which parses every
file in `.github/workflows` and rejects any artifact-writing job whose transitive
`needs` closure does not contain `release-gates`. A job counts as artifact-writing
when it holds `packages`, `id-token`, or `attestations` write permission (or the
scalar `permissions: write-all`, which grants all three), when a step mutates a
GitHub release, publishes an npm package, pushes an image, sends a
`repository_dispatch`, or attests provenance, or when it `uses:` a publishing,
release, or repository-dispatch action — those authenticate with a secret in
`with:` rather than through `permissions`, so nothing else would see them.
(`contents: write` alone does not count: `dep-audit.yml` holds it only to push a
remediation branch.)

The same check fails if `authz-coverage` — or any other gate in `REQUIRED_GATES`
— is dropped from the aggregate's `needs` or removed from the workflow, if the
aggregate would skip past a failed gate, if it stops calling `decide`, or if a
workflow becomes unparsable. `node --test scripts/ci/release-gates.test.mjs`
covers both halves against fixtures, including a synthetic future publisher wired
to the wrong dependency, and cross-checks the bundled workflow reader against an
independent scan of the raw text so a job the reader silently drops fails the
suite rather than vanishing from the graph.

The same job also enforces that every action the workflows call is pinned to a
40-character commit digest, with the human-readable version in a trailing
comment. A mutable tag is a standing write into this repository's build by
whoever can move it upstream — including into `release-gates` itself, the one
job whose verdict authorizes publication. Only a `./`-prefixed action from this
repository is exempt, since it is already as trustworthy as the tree calling it.
The scan covers a job's own `uses:` as well as its steps', so a reusable
workflow cannot enter unpinned either. Pin a new action to the digest its tag
resolves to — `gh api repos/OWNER/REPO/git/ref/tags/TAG --jq .object.sha`.

Two limits are worth stating plainly. The contract **cannot protect its own job**:
delete `release-contract` from the workflow and both the check and its tests stop
running, with nothing in-repo left to notice — branch protection is the only
backstop for that, and it is why `release-contract` belongs in the repository's
required-checks list alongside `release-gates`. And artifact-writing detection is
a pattern list, not a proof: a publisher that writes by some means outside the
signals above would not be classified. Extend `PUBLICATION_STEP_PATTERNS` and
`PUBLICATION_PERMISSIONS` when a new publication mechanism arrives.

## What Codefly owns

The complete gate runs these ordered phases:

1. base integrity verification;
2. non-mutating generated-source drift detection;
3. plugin-owned lint;
4. plugin-owned native compilation or typechecking;
5. plugin-owned named test suites;
6. plugin-owned dependency and vulnerability audit;
7. plugin-owned CycloneDX SBOM generation; and
8. plugin-owned deployable artifact/container build.

Codefly computes directly changed services and their transitive dependents from
the workspace graph. Independent tasks run concurrently, while tasks that share
runtime dependency closures are serialized. Pushes and pull requests provide a
base revision; manual runs and first pushes fall back to `--all`.

Language commands, protobuf toolchains, dependency lifecycle, container build
logic, and evidence normalization belong to the service plugins. If a required
gate is missing, extend the generic Codefly/Core contract and the applicable
plugin; never add a repository-specific implementation to provider YAML.

## Canonical manifest freshness

Phase 1 verifies that base files match `tools/base-manifest.json` — it trusts
the committed manifest as the source of truth. That protects consumers, but it
cannot catch the manifest itself drifting from the canonical tree it was
generated from: a base file edited without re-running
`node tools/base-integrity.mjs gen` ships a stale digest, and every consumer
sync then aborts on an unreconcilable source-path mismatch (v0.0.32).

Provider CI closes that gap with `base-integrity.mjs verify`, which re-derives
the manifest from the tree and fails on any changed, unrecorded, or removed base
file. This is the one repository-specific gate in provider YAML: it guards the
canonical artifact that seeds every other consumer, so it must run here rather
than in a consumer copy.

## Authorization coverage

The generated authorization catalog
(`services/accounts/generated/authz-methods.json`) is a complete, typed policy
for every RPC. The `Authorization coverage` job turns that catalog into
default-deny CI gates, so an under-specified or widened route is un-mergeable
rather than merely discouraged. `tools/authz-coverage-gate.mjs` runs, in order:

- **RBAC coverage** (`rbac`) — every RPC must declare a coherent gate: a known
  exposure (public / authenticated / internal), a known policy tier served on
  the matching exposure, and a platform-role requirement iff it is a
  platform-admin route. An unclassified route fails.
- **Audit coverage** (`audit`) — every mutating, caller-attributable RPC must
  emit audit. A mutation that records nothing fails.
- **Permission no-broadening** (`no-broadening`) — the catalog is diffed against
  `main`; any change that widens who may call a route (a relaxed exposure,
  tenant, platform-role, or MFA requirement, or a dropped permission/scope) fails
  unless the pull request carries the `authz-broadening-approved` label, which
  sets `AUTHZ_ALLOW_BROADENING` for the run.

Both coverage gates read ticketed exemptions from
`tools/authz-coverage-allowlist.json`; every entry needs a reason and a ticket,
and removing one re-arms the gate. The same job also runs the sidecar
header-lockstep test (`TestUntrustedHeaders_SupersetOfStampedHeaders`), which
keeps the gateway's stamped identity headers a subset of the headers it strips;
the accounts-side companion (`TestUntrustedHeaders_SupersetOfTrustedHeaders`)
runs with the accounts service test suite.

## Published frontend kit version

A registry version is immutable, so the frontend kit's version has to move
whenever its content does. `publish-frontend-kit.mjs` enforces that at the
registry — it compares the built tarball's integrity against the version already
published and refuses to contradict it — but on its own it only reports the
problem at release time, after the tag is cut, which is where v0.0.58 stopped
with the kit's content several releases ahead of the version the registry serves
(#550). Meanwhile a consuming solution that installs the kit resolves the older
published bytes while the host serves its newer workspace copy, and the two
share one Module-Federation singleton slot keyed by version, so nothing shows
until an export disappears at runtime.

`kit-version` moves that verdict onto the pull request. It takes the newest
deploy-counter tag as the baseline of what the registry serves and fails when a
published kit package's content changed since that tag under an unchanged
version:

```sh
node scripts/ci/kit-version.mjs check
```

It reads git history rather than the registry on purpose — a registry read needs
a publish-capable token, which no branch build should hold. The registry
comparison in the publish step stays as the exact authority at release time.

The kit is co-versioned: `@codefly-dev/ui`, `@codefly/saas-ui`, and
`CODEFLY_KIT_VERSION` in `services/frontend/code/src/solutions/SolutionOutlet.tsx`
bump together, which the `kit-shared-version` test pins.

## Evidence

Every run writes the schema-versioned report to `.codefly/ci/report.json` and
places SBOMs and other plugin-returned artifacts below the same directory. The
report records the affected-service plan, task graph, resolved plugin metadata,
phase and suite identities, timings, outcomes, blocking relationships, and
artifact hashes.

The report directory is machine-local output and is not committed. A CI
provider may retain it without interpreting or reconstructing its contents.

## Staying ahead of newly-published advisories

The vulnerability audit (phase 6, and the first-party gate in `ci.yml`) fails
closed on every high-severity finding in the production dependency tree. Because
that check reads the live advisory database, a freshly published advisory on an
already-pinned transitive can redden an otherwise-clean release at tag time even
though the lockfile never changed (browserslist did exactly this before #400).

`.github/workflows/dep-audit.yml` keeps main ahead of that. On a daily schedule
(and on demand via `workflow_dispatch`) it runs `npm audit fix
--package-lock-only --omit=dev` across the frontend and marketing lockfiles,
rolls whatever it can safely remediate into a single standing pull request
(`chore/dep-audit-remediation`), and then re-runs the gate's own
`--audit-level=high` audit. If an advisory cannot be auto-fixed — it needs a
major bump or an explicit `overrides` pin — that final step fails the run so a
maintainer acts before the next tag. The job never weakens the gate: it moves
the same policy earlier so tags cut from an already-remediated tree.

## Local use

Run the same release gate from the workspace root:

```sh
codefly ci run --all
```

For a focused reproduction, use `--phase`, `--suite`, or a base revision. These
are views of the same plugin-owned gate, not alternate test pipelines.

## Release cadence and ownership

`codefly sync module` pins an **immutable semver tag** in the consumer's
`base-source.json` lock. Consumers advance only when they deliberately re-pin,
so this repository's tag rhythm bounds every consumer's update rhythm.

Tags are cut by the saas-starter maintainers **on demand** — whenever
consumer-relevant base changes have landed on `main` and pass `codefly ci run`
plus `base-integrity.mjs verify`. There is no fixed calendar; a release is a
maintainer decision that the current base tree is a good pin, not a scheduled
event. Consumers that need to move faster than tags are cut should open an issue
rather than pin an untagged revision.

Two independent tag tracks share this repository, on two different version
axes. They are not interchangeable:

- **Deploy counter** — the `v0.0.x` tag series lodestar and the per-environment
  deploy jobs adopt via `codefly sync module --to <tag>`. The tag itself is the
  counter; `agent.codefly.yaml`'s `version:` is the module agent's own version
  and may lag the tags (it is bumped when the agent changes, not on every tag).
- **Immutable module package** — `module-package/vX.Y.Z`, sourced from
  `module/module.package.codefly.yaml`'s `version:` (the module semver). Only
  this track triggers the immutable-package publication job (strict manifest
  validation, SBOM, provenance signing). The two axes are genuinely different;
  do not conflate them (that mismatch was [#405]).

To cut a deploy-counter tag:

1. If the module agent itself changed, bump `version:` in the root
   `agent.codefly.yaml`.
2. Commit it as `release: v0.0.N`.
3. Tag that commit with an annotated `v0.0.N` tag and push the tag.

To cut an immutable module-package release:

1. Bump `version:` in `module/module.package.codefly.yaml`.
2. Commit it as `release: module-package/vX.Y.Z`.
3. Tag that commit with an annotated `module-package/vX.Y.Z` tag and push it.

[#405]: https://github.com/codefly-dev/module-saas-starter/issues/405
