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
claim to measure either. The pinned Core SDK reports only flow-wide readiness:
per-service build/setup durations and the exact service that exceeded its budget
still require SDK/CLI lifecycle evidence. Keep the Codefly debug log with failures.

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
  [Core #455](https://github.com/codefly-dev/core/pull/455), still tracks released
  agent discovery adoption.
- [CLI #611](https://github.com/codefly-dev/cli/issues/611) shipped prerequisite
  scheduling safety. [CLI #626](https://github.com/codefly-dev/cli/issues/626)
  owns the remaining typed suite selection and validated plan execution.

No provider workflow maintains a second test matrix. Planner consumption of this
catalog remains pending those agent/CLI integrations.

## Database qualification

These package tests exercise the candidate schema. They do not establish full
reference-to-candidate upgrade qualification. Keep clean installation and upgrade
as independent required results in the migration gate owned by
[#644](https://github.com/codefly-dev/module-saas-starter/issues/644), scoped to
migrations, migration runners, store images and their configuration/toolchains.
A fast pure result cannot satisfy either result. Until reference-aware qualification
ships, neither this catalog nor these test runs claim that coverage.
