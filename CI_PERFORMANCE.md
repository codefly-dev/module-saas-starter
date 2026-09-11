# CI performance investigation

Target: less than five minutes from workflow creation to the successful release
gate on an ordinary code change, with every existing check retained. Publication
steps are additional. Queue time counts toward the target.

## Measured baseline

Two successful runs sampled on 2026-09-11 UTC:

- [Main run](https://github.com/codefly-dev/module-saas-starter/actions/runs/34550580028): 11m00s.
- [Pull request run](https://github.com/codefly-dev/module-saas-starter/actions/runs/34551079489): 9m25s.

Durations below come from GitHub job/step timestamps and Codefly phase logs,
not local estimates. Jobs overlap; their durations must not be summed.

| Work | Main | Pull request |
| --- | ---: | ---: |
| Plan job | 30s | 34s |
| Quality job | 613s | 511s |
| Quality disk cleanup | 133s | 27s |
| Quality commands | 451s | 434s |
| Build job | 427s | 360s |
| Build disk cleanup | 86s | 26s |
| Build command | 304s | 298s |
| Supply chain job | 143s | 195s |
| SDK boundary job | 131s | 152s |
| Authorization coverage job | 105s | 106s |

The main quality command ran sequential phase barriers: approximately 57s for
verify/drift, 113s lint, 85s compile, and 187s test, plus command setup. The
test phase itself serialized accounts before gateway before frontend. Accounts
tests consumed about 97s and frontend tests about 81s.

The main build phase lasted 298s after command initialization. Frontend did
not start until 180s into that phase, following accounts and gateway builds.
Frontend then took 118s. The workflow already sets `--jobs 2`; more workers
alone cannot eliminate this ordering. The pinned CLI schedules affected services
through the dependency graph. Docker layers are reused within a job but the
workflow has no cross-run image build cache.

Authorization coverage disabled Go caching and spent 83s in two targeted Go
test commands. SDK boundary also disabled caching and spent 70s compiling the
CLI on the main sample (86s on the PR). The other Codefly jobs restored an
approximately 408 MB Go cache keyed only to the root module's checksum file.

## Changes in this checkout

1. Fan quality out into four independent phase jobs. Keep the original affected
   selections and fixture, and require every matrix result through the existing
   release aggregate. This removes the sum of phase barriers from the workflow
   critical path. It adds three runner allocations and may increase total
   runner minutes and queue pressure.
2. Replace unconditional deletion of three toolchains with a free-space check.
   Stop deleting once Docker has 12 GiB available; fail if cleanup cannot supply
   it. Only the test quality lane performs this preparation. Full-image builds
   still need a hosted-runner check to confirm this threshold provides enough
   headroom throughout the job.
3. Enable the disabled Go caches and include all independent Go module checksum
   files in the heavy jobs' cache keys. Cache npm downloads across quality
   runners while keeping clean dependency installation owned by each agent.

4. Install the published Linux x64 Codefly binary with a checked-in SHA-256
   digest instead of compiling the same pinned CLI independently in every job.
   The archive digest was checked against the downloaded v0.1.145 release.

Standalone phases start cold and cannot reuse installs or compiler output from
earlier phases in the same run. No tests, audits, SBOMs, image builds, or
release requirements were removed.

## Hosted result

One complete hosted run on commit `b0b521e4`,
[run 34553269981](https://github.com/codefly-dev/module-saas-starter/actions/runs/34553269981),
succeeded in **6m19s** from workflow creation to the release gate, with every
mandatory gate green. This is a single run, not a repeated benchmark, and it
does not establish cold-versus-warm cache behaviour or the disk peak of a
full-topology build.

| Work | Baseline samples | This run |
| --- | ---: | ---: |
| Combined quality / longest parallel phase | 8m31s-10m13s | 4m50s (test) |
| Quality aggregate | n/a | 2s |
| Build job | 6m00s-7m07s | 5m22s |
| SDK boundary job | 2m11s-2m32s | 55s |
| Authorization coverage job | 1m45s-1m46s | 24s |
| CLI installation | up to 86s compiling | 1-2s download and verify |
| Disk preparation | 26-133s | 0-2s |

The other three quality phases finished well inside the test lane: 1m23s for
verify/sync-drift, 2m04s lint, 2m51s compile. Against the 9m25s and 11m00s
samples above that is roughly 33-43% faster, but the five-minute target is
still unmet and the build job is now the critical path, which is what the
remaining work below addresses.

## Remaining work to reach five minutes

The current image build is already almost five minutes before planning, runner
setup, and the aggregate. Workflow-only cleanup cannot meet the target for the
sampled full build.

1. Change Codefly's generic build scheduler to distinguish runtime dependencies
   from artifact prerequisites. Schedule independently buildable service images
   concurrently while preserving actual artifact prerequisites. Validate this
   in the CLI/Core scheduler tests before adopting a new published version here.
   The measured frontend's 180s wait is the first target.
2. Add supported cross-run BuildKit cache import/export to the generic build
   contract and agents. Cache dependency layers separately from application
   source; preserve cold-cache correctness. Do not implement replacement Docker
   builds in this provider workflow.
3. If standalone tests remain over budget, add supported service sharding with
   isolated runtime dependencies and ports. Keep accounts and frontend's
   dependency-backed tests; do not remove topology locks on a shared runner.

A working budget is 30s planning, 30s setup, at most 210s for the longest parallel
lane, and 15s aggregation/queue allowance: 285s total. This is a target, not a
forecast. Compare warm and cold code-change runs, full-topology runs, and queue
delay separately; require repeated measurements below 300s before claiming the
goal is met. Larger runners are an option if CPU remains limiting after these
changes, but no runner purchase or configuration change is included here.

## Validation

`node --test scripts/ci/release-gates.test.mjs scripts/ci/ci-performance.test.mjs`
checks the complete gate contract, phase selection and failure propagation,
and disk cleanup's sufficient-space, recovery, and exhaustion cases using fake
commands. `node scripts/ci/release-gates.mjs check` validates publication
dependencies and action pins. `actionlint .github/workflows/ci.yml` validates
the provider workflow. Full hosted CI has run with these changes; see
"Hosted result" above.
