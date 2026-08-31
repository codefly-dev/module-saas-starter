# ADR 0005: Delivering the typed-settings library to more languages (Python) — a module concern, not a core one

- Status: Proposed
- Date: 2026-08-29
- Task: records the delivery-mechanism analysis behind #346 (Python settings
  delivery), #347 (org-scoped generic settings), and
  codefly-dev/saas-sdk-go#3 (promote the renderers + runtimes to the shared
  SDK). It authorizes the split below but changes no code.

## Context

The typed user-settings system (`SETTINGS.md`, built under #100) already does
what a settings library should: a product declares settings in protobuf,
other repos **contribute** fields through the composition/contribution
contract (`settings-contribution.schema.json`), and the build generates a
typed field catalog per language plus a composed proto embedded at field 1000
of `UserSettings`. Storage is sparse ProtoJSON in a JSONB column; the wire
surface is one generic `Get`/`Update` RPC, so adding a setting is **proto +
regen only** — no gateway or endpoint redeploy.

Today that library is delivered to **Go** and **TypeScript**. The open
question raised while planning Python (#346) was: *is delivering another
language a codefly-core capability, or a module one?* This ADR answers it,
because the answer determines where the Python work — and any future language —
lands.

### What core owns (the delivery pipeline)

The delivery mechanism is codefly **core**'s composition engine
(`core/composition`, pinned via `module/tools/go.mod`). Per `engine.go` and
`projection.go`, composing a module:

1. resolves and verifies the immutable module package (signature, digest,
   moved-tag rejection) and materializes a read-only base cache;
2. copies the base into a disposable **projection**, writes
   `.codefly/composition.input.json` (a `CompositionInput/v2` carrying the
   consumer's `contributions.settings: [{path, message}]`), and exports
   `CODEFLY_COMPOSITION_*` environment variables;
3. runs the module's declared `generators`, **in order**, as subprocesses
   inside the projection (`Renderer.runPackageCommand`);
4. loads `composition.catalog.json` and runs collision detection
   (`ValidateCatalog` over digests, `ValidateCollisions` over claims —
   `settings-field` is a core `CollisionKind`), then the module's
   `conformance` suites and integration tests;
5. promotes the staging projection to a content-addressed, read-only revision
   (atomic symlink swap) and writes the lock.

The decisive fact: **core has no language awareness.** A search of the entire
`core/composition` package for `python|typescript|protoc|buf|npm|golang`
returns zero hits. Core is a generic command-runner plus a
collision/projection/lock framework. It runs whatever `generators` a module
declares and collision-checks a generic catalog of claims and output files.

### What the module owns (language rendering)

Every language-specific decision is module-side, in exactly three kinds of
artifact:

- **Renderers** — `renderSettingsGo` / `renderSettingsTypeScript` /
  `renderSettingsProto` in `module/tools/composition/composition.go`.
- **buf plugins** — `module/services/accounts/buf.gen.composition.yaml`
  (currently `protoc-gen-go` + `protoc-gen-es`).
- **Schema-agnostic runtimes** — Go `pkg/settings/{field.go,json.go}` and TS
  `packages/saas-settings/src/index.ts`, which `SETTINGS.md` says are copied
  unchanged into products.

The module wires these together by declaring `generators` in
`module.package.codefly.yaml` (`composition` → `module-compose`,
`composed-protobuf` → `buf`, `composed-go-format`).

## Decision

**Delivering a new language is a module concern; codefly core needs no change.
Add Python the same way Go and TypeScript exist — a renderer, a buf plugin, and
a hand-written schema-agnostic runtime — and, because those language parts are
reusable across modules, extract them into the shared SDK rather than letting
each module vendor its own copies.**

### Q1 — Does core change to add Python? No

Core already delivers any artifact a generator emits and collision-checks a
generic catalog. It will deliver a Python library the moment the module emits
one. Adding a language target to core (a "supported languages" registry, a
built-in codegen step) would couple the language-agnostic engine to a fixed
set and buy nothing. Rejected.

### Q2 — The three module-side additions for Python (#346)

1. `renderSettingsPython` in `composition.go`, beside the Go/TS renderers,
   emitting a Python field catalog (e.g. `field_catalog.py` exporting
   `SETTINGS_FIELDS`), wired through a new `SettingsPythonOutput` in
   `render()`.
2. A Python plugin in `buf.gen.composition.yaml`
   (`protoc-gen-python`/`protoc-gen-pyi` or betterproto) and a
   `composed-python-protobuf` entry in the `generators:` block, so the composed
   and contributed protos compile to Python during composition.
3. A `saas_settings` Python runtime mirroring `pkg/settings` and
   `@codefly/saas-settings`: a presence-aware `Field` (get / set / clear /
   path / lookup, preserving absent-vs-explicit-zero) plus a ProtoJSON codec,
   schema-agnostic and copied unchanged into products.

### Q3 — Where do the renderers and runtimes live? The shared SDK (saas-sdk-go#3)

The renderers and runtimes are the same for every module. Leaving them in
saas-starter forces Warden and any other product to re-implement them, which is
where three-way drift (Go/TS/Python × N modules) would start. Move them into
the shared SDK (`saas-sdk-go`, whose plan already records "go/python first"),
version and publish them, and have modules depend on them. Core's
generator/projection contract is unchanged; modules keep declaring
`generators`. Do this extraction **before** Python is duplicated across
modules.

### Q4 — Packaging for third-party contributors: out of scope here

The base runtime and generated catalog are plain files in the projection and
need no install step. Only if third-party repos should **contribute** Python
packages (the way they contribute frontend plugins today via
`updateFrontendInstallGraph` / `stageContributionSources` running npm) would a
pip/uv install-graph staging step be required. That is a separate concern from
delivering the base library and is not decided here.

## Options considered

- **A. Add Python codegen support to codefly core.** Rejected — couples a
  deliberately language-agnostic engine to a fixed language set; buys nothing
  the generator contract doesn't already provide.
- **B. Add the Python renderer + runtime inside saas-starter only.** Correct
  and shippable, but re-implemented per module → Go/TS/Python drift. Acceptable
  as an interim if the extraction (Q3) is not ready, but not the target state.
- **C. Module-side additions, with renderers + runtimes in the shared SDK.**
  (Chosen.) No core change; one implementation of each language, reused by
  every module.

## Consequences

- **Core is untouched.** The composition engine keeps its language-agnostic
  generator/projection/collision/lock contract; adding Python (or any later
  language) never reaches it.
- **Python joins Go and TS as a first-class target** via one renderer, one buf
  plugin, and one runtime (#346).
- **Org-scoped settings (#347) inherit the same delivery** for free — they are
  another contribution surface over the identical machinery, so whatever
  languages the renderers target, org settings get them too.
- **One implementation per language.** Once saas-sdk-go#3 lands, saas-starter
  consumes the shared renderers/runtimes instead of vendoring them, and new
  modules get typed settings in every supported language without new code.
- **Sequencing.** Like ADRs 0003 and 0004, this authorizes but does not perform
  the change. Prefer landing the SDK extraction (saas-sdk-go#3) before or with
  the Python renderer so the Python runtime is written once, in the shared
  package, rather than in saas-starter and then moved.
