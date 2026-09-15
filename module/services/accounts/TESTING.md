# Accounts test targets

Run commands from `module/services/accounts/code`:

| Target | Command | Runtime services | Dependency readiness budget |
| --- | --- | --- | --- |
| Pure | `go test -tags=pure ./...` | None | None |
| Business database | `go test ./pkg/business` | store, vault | 300s |
| Infra database | `go test ./pkg/infra` | store | 90s |
| Authentication database | `go test ./pkg/auth/pg` | store | 120s |
| Billing database | `go test ./pkg/billing/pg` | store | 120s |

For a source-inspection gate, use for example
`go test -tags=pure ./pkg/business -run '^TestAuditDurability' -count=1`.
The pure target uses Go build constraints to exclude database test files and
package startup. It includes in-memory unit tests, HTTP fixtures, audit source
inspection and the RLS test-source guard. New database tests that consume the
shared fixtures must declare `//go:build !pure`; otherwise the pure build fails
because those fixtures are unavailable. Do not combine `pure` with `integration`.

The default `go test ./...` and `codefly ci run` retain all existing default tests.
The pure command is an additional fast check, not a replacement release gate.
Database targets also retain their package's pure tests. They serialize setup,
execution and teardown through the existing package lock. Business tests use
Vault for hashing and encrypted credentials. None of these suites uses telemetry
or Redis; dependency exclusions prevent those services from building or starting.

With `go test -v`, database harnesses emit JSON timing records to stderr for
`dependency-setup` and `test-execution`, naming the suite, requested services,
elapsed milliseconds, readiness budget and failure status. A failed dependency
setup emits no execution record. Go compilation precedes these records and
fixture initialization sits between setup and execution. These records do not
claim to measure either. Core 0.3.29 now carries per-service readiness in
`FlowStatus.services`. These timing wrappers receive only their requested graph,
not a status snapshot, so they still must not infer which service overran. Keep
per-service diagnostics from the CLI alongside the timing records; the obsolete
single-field descriptor assertion has been removed.
Keep the Codefly debug log with failures.

### Isolated request-pool qualification

From the repository root, with native PostgreSQL binaries, a Go 1.27 executable
and cached dependencies:

```sh
python3 scripts/qualify-scoped-pools.py \
  --postgres-bin /path/to/postgres/bin --go /path/to/go --transport verified-tls
```

This extra qualification invokes the public production constructor against a
temporary loopback-only TLS PostgreSQL cluster. `--transport legacy` repeats the
same proof with the unchanged empty-profile behavior. It checks verified organization/user
scope and pooled reuse, explicit legacy/control-plane role behavior, rejection of
an old credential by PostgreSQL, token rotation after backend termination, repeated
close and cleanup after failed writer startup. The owner connection uses fixture
trust; application roles must authenticate using SCRAM. The cluster is removed
after the run. No cloud issuer, provider call or deployed transport is exercised.

The test lives in `qualification/scopedpools`, outside the shared integration
package startup. It skips without the runner's explicit disposable-fixture DSN
and is excluded from `pure`; it does not replace the canonical service gate.

## Standalone dependency-harness prerequisite

The PostgreSQL runtime dependency now selects Core 0.3.29. Its `WithDependencies`
uses an isolated Unix control session and `SessionHandshake` when a standalone
Go test owns the dependency graph. The official CLI 0.1.145 pinned by the managed
CI installer selects Core 0.3.20 and does not provide that handshake. It is not a
compatible standalone graph owner for this SDK. Do not opt out of isolation to
hide that mismatch.

The actual Codefly quality(test) run on 2026-09-14 confirmed that these database
packages create nested graph-owning sessions even under CI. They did not reuse a
managed parent: CLI 0.1.145 failed to bind the requested isolated socket for all
four database packages. The module's unchanged 90s/120s/300s setup budgets exposed
the incompatibility before database test execution.

Only the quality(test) matrix job now builds canonical CLI commit
`7a3a895a1ea308ea838e9e165221fc875d2563bb` (Core 0.3.29), using the pinned Go 1.27.0
toolchain. `scripts/ci/install-codefly-test.sh` verifies the exact commit and tree,
uses read-only module resolution and VCS build metadata, and records the binary
hash and build information. The source tool is installed after runner disk
preparation. The other jobs retain the official CLI 0.1.145 installer; deployment
CLI pins are unaffected. The full test selection, gates, isolation and timeouts
remain unchanged. This source build is not a newer official CLI release.
Standalone graph-owning tests need this compatible tool plus their normal
container/agent prerequisites. Pure tests, compilation and native database
qualifications alone do not establish that the complete dependency graph boots.

On 2026-09-14, that exact CLI source was built locally with Go 1.27.0 on
darwin/arm64 using `go build -mod=readonly -ldflags='-s -w' ./cmd/codefly`.
Its embedded build information confirmed Core 0.3.29 and an unmodified source
tree. The real-server tests `TestServerSupportsCoreIsolatedSession`,
`TestSessionEnvironmentCannotFallBackToTCP` and
`TestIsolatedSessionDoesNotUnlinkForeignSocket` all passed in `./pkg/web`.
This verifies the local Unix-socket session prerequisite only: it is neither an
official CLI release nor proof that the full Accounts dependency graph boots.

## Planner handoff

[`test-targets.json`](./test-targets.json) is a versioned repository catalog of
commands, runtime prerequisites and conservative repository-relative input roots.
Roots ending in `/` include descendants; other roots name exact files. Accounts
source includes its audit registry, generated bindings, fixtures and lockfiles.
Shared proto, migrations/RLS, generators, configuration and topology invalidate
every listed target. Store and Vault implementation changes additionally affect
database targets. Unknown inputs require whole-service verification with its
dependency closure. Compare both reference and candidate inventories, including
removed paths, renamed files, symlink targets and removed dependency edges.

This catalog is **not** a complete Core `TaskInputs` declaration or a CLI plan.
All entries deliberately carry `complete: false`: there are no resolved native
imports, toolchain/plugin identities or protected environment identities here.
It must not enable result reuse or narrow the canonical gate. The Go agent must
advertise suites independently, resolve these application inputs together with
native inputs, and transport them through Core's `Agent.GetEffectiveInputs` v1.
Unknown/legacy discovery retains Core's whole-service fallback.

Coordination contracts:

- [Core #445](https://github.com/codefly-dev/core/issues/445), implemented in
  [Core #455](https://github.com/codefly-dev/core/pull/455), released schema v1 of
  `Agent.GetEffectiveInputs`: a task key is a phase plus a test suite,
  `complete: false` forces conservative selection and bars result reuse, and
  `runtime_services` states only what must be running, never what content a task
  consumes.
- [CLI #611](https://github.com/codefly-dev/cli/issues/611) and
  [CLI #626](https://github.com/codefly-dev/cli/issues/626) are both closed:
  prerequisite scheduling, phase graphs and validated plan execution shipped.
- Released-agent adoption is the remaining blocker. Core's effective-input
  documentation assigns it to `codefly-dev/service-go` and
  `codefly-dev/service-nextjs`, which must implement discovery and
  framework-specific conformance before advertising v1. Until one does, Core
  keeps conservative whole-service behavior.

No provider workflow maintains a second test matrix. Nothing consumes this
catalog yet.

## Database qualification

These package tests exercise the candidate schema. They do not establish full
reference-to-candidate upgrade qualification. That qualification shipped with
[#644](https://github.com/codefly-dev/module-saas-starter/issues/644) as
independent required results in the **Base manifest integrity** context, scoped to
migrations, migration runners, store configuration, the managed install baseline,
the store image recipe and the migration gates themselves. [`../store/test-targets.json`](../store/test-targets.json) declares
those two checks as separate planner targets carrying that input scope. A fast
pure result cannot satisfy either result, and no accounts target claims it.
