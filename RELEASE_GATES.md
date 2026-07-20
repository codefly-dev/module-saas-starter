# Release gates

The SaaS Starter has one canonical release gate: `codefly ci run`.

GitHub Actions is only a provider adapter. It checks out the repository,
installs the pinned Codefly CLI, supplies the base and head revisions, and
invokes the gate. It does not encode Go, Rust, Next.js, protobuf, dependency,
container, or service-specific commands.

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

## Evidence

Every run writes the schema-versioned report to `.codefly/ci/report.json` and
places SBOMs and other plugin-returned artifacts below the same directory. The
report records the affected-service plan, task graph, resolved plugin metadata,
phase and suite identities, timings, outcomes, blocking relationships, and
artifact hashes.

The report directory is machine-local output and is not committed. A CI
provider may retain it without interpreting or reconstructing its contents.

## Local use

Run the same release gate from the workspace root:

```sh
codefly ci run --all
```

For a focused reproduction, use `--phase`, `--suite`, or a base revision. These
are views of the same plugin-owned gate, not alternate test pipelines.
