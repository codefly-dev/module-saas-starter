# Release gates

The SaaS Starter has one canonical release gate: `codefly ci run`.

GitHub Actions is only a provider adapter. It checks out the repository,
installs the pinned Codefly CLI, supplies the base and head revisions, and
invokes the gate. It does not encode Go, Rust, Next.js, protobuf, dependency,
container, or service-specific commands.

For version tags, successful completion of this gate unlocks the immutable
module-package publication job. That job handles only the release transport:
strict package-manifest validation, deterministic archive construction, digest,
aggregate SBOM, provenance signing, and immutable GitHub Release publication.
It does not duplicate service build or test policy.

Publication requires three release-only repository secrets: the read-only
Administration token `RELEASE_ADMIN_TOKEN` for immutable-release policy checks,
the base64 Ed25519 key `RELEASE_PROVENANCE_PRIVATE_KEY` for Core's detached
module provenance signature, and its independently configured trust-policy key
`RELEASE_PROVENANCE_PUBLIC_KEY`. Missing, malformed, or mismatched credentials
fail before a release is created.

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
