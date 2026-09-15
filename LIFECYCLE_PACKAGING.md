# Packaging the declared lifecycle command

Status: proposed owner correction; adoption awaits a published service-agent release.

The topology declares a `role-catalog-import` deploy Job using the Accounts image.
The pinned `go-grpc` agent `0.1.36` builds only the service executable, so the
checked-in Accounts recipe cannot satisfy that Job. The importer already belongs
to Accounts at `module/services/accounts/code/cmd/role-catalog-import`.

The proposed correction adds an explicit command through the Go service agent's
existing build settings. It preserves the declared Job, its store dependency,
catalog input, authority and ordering. Once a released agent supports the setting,
the Accounts `spec` in `module/deployment/topology.bindings.codefly.yaml` would add:

```yaml
build-commands:
  - name: role-catalog-import
    package: ./cmd/role-catalog-import
```

The agent builds this main package under the same Go module locks, toolchain,
architecture and CGO setting as the service, installs it on the image's PATH,
and refuses invalid or unavailable package declarations. This declaration is
deliberately not active while the service pins an agent that does not implement it.

## Release and verification order

1. Review and publish the service-agent command packaging change and record the
   exact released version and source commit.
2. Update the Accounts agent pin and add the above setting in authored topology.
   Regenerate through `go generate ./pkg/cataloggen` from
   `module/services/accounts/code`; refresh the base manifest from a clean tree.
3. Generate a fresh Accounts build recipe through the selected released agent.
   Confirm both the executable's build and final-image copy. Build the actual
   image and execute its importer under the deployment architecture and user.
4. Generate the selected Postgres owner's bootstrap recipe against this exact
   store migration tree. Compare the staged SQL names and per-file checksums,
   ordered versions, generated runtime-access SQL and role catalog identities.
   Compare SQL inventory, not a historical count or the migration README.
5. Exercise fresh schema creation, importer application, unchanged replay and a
   supported prior-release upgrade with retained data against real Postgres.
   Record source, recipe, image and database identities alongside the outcomes.
6. Publish the module release only after its normal gates succeed; consuming
   workspaces then adopt the release through base-sync and qualify it themselves.

## Existing migration owner

Postgres owns the reusable bootstrap renderer and runner. Its current source
already stages every resolved migration lineage, emits `plan.json` and
`sources.sha256`, checks staged contents, and copies only those generated artifacts
into the image. The module owns the canonical store SQL and its application-level
replay tests. This change does not create a second migration renderer or alter a
stored migration to match an old recipe.

The checked-in builder snapshots alone do not establish what the selected released
agents will render. The proposed generator now has opt-in tests that call its Build
RPC, build the exact returned recipe, and execute its command and source-relative
assets in a non-root container. A second test accepts a real service source path
and executes its declared command's help without database effects. The recipe
retains source hashes, unchanged Go locks, selected module graph, compiled binary
metadata and hashes of final runtime files under `/usr/share/codefly/build`.
Acquisition verifies the Go cache and lock continuity; compilation and evidence
collection run with network disabled. These retained bytes do not assert scan,
publication, feed approval or installed-service identity.

The existing Postgres owner parity tests run schema setup twice through both
its local runner and built bootstrap image. Those owner fixtures do not establish
this module's full canonical migration/import/recovery behavior. The release gate
still requires the actual source and database tests described above.

For paired npm inputs, the module distributes the reusable offline CLI described
in [NPM_INTAKE.md](module/tools/NPM_INTAKE.md). It accepts registry/scoped routing
as data and preserves original integrity across both host and remote builds.
An unavailable private artifact remains an explicit input blocker; independently
rebuilding a UI package requires a new identity and reviewed consumer lock change.
