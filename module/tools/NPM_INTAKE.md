# Preparing npm inputs through an approved registry

`npm_intake.py` is a distributed build-input CLI, shipped beside the module's
workspace-lock tooling. Both this module's frontend and independently built
remote frontends can invoke the same pinned file; no host runtime imports or
knowledge of the remote's implementation is involved. Its small offline contract
can move to shared Codefly build tooling without changing the caller's inputs.

```sh
python3 /path/to/pinned/module/tools/npm_intake.py \
  --lock /source/package-lock.json \
  --lock-sha256 <reviewed-source-lock-hash> \
  --manifest-sha256 <reviewed-source-manifest-hash> \
  --profile /reviewed/npm-routing.json \
  --intakes /reviewed/intakes.json \
  --output /new/prepared-inputs
```

Omit `--intakes` when no exceptional archive is present. The routing JSON contains
`registry` and optional `scopes`, using credential-free HTTPS prefixes ending in
`/`. Example: `{"registry":"https://registry.example.com/npm/","scopes":{"@example":"https://packages.example.com/npm/"}}`.
An optional `install_options` object preserves explicitly selected boolean
`legacy-peer-deps` and `strict-peer-deps` settings. Callers must carry their
reviewed install mode; the tool does not inspect potentially secret-bearing
user or source npm configuration.

An intake maps an exact original URL to `destination`, a local regular `artifact`,
its independently reviewed `sha256` and its unchanged original `integrity`.
Every recorded SRI digest must match those bytes. Rebuilt packages with different
bytes need a distinct package identity and reviewed consumer lock change. This
command will never relabel them as the original private artifact.

Installed packages require an original source URL and integrity. Root and linked
workspace records are local; a bundled dependency may omit its own URL only when
its exact enclosing archive is integrity-pinned and explicitly lists that bundle.
Nested archive override values follow the same reviewed route as dependencies;
ordinary version overrides and `$dependency` references are preserved. Unmapped
archive overrides and archive-qualified override selectors are refused before output.

The output retains root and workspace manifests paired with the lock, token-free
registry configuration, and provenance hashing all inputs and prepared outputs.
Only routing declarations change. Overlay these files into a fresh source copy,
verify the recorded hashes, then run the existing frontend build entrypoint with
its approved acquisition configuration. Never copy them over immutable module
base files. Existing output directories and symlinked inputs are refused.

This command does not acquire, install, build or publish. An authenticated
`npm ci`, actual package/export build, host/remote singleton compatibility,
runtime architecture checks, scans and publication receipts remain separate.
Source hashes and provenance identify preparation, not artifact approval or an
installed product. Run the hermetic CLI tests with
`python3 -m unittest discover -s module/tools -p test_npm_intake.py`.
The same command runs in the required base-integrity CI job on pull requests,
merge-queue groups and release tags. Optional real-source intake checks still
require the caller's explicit source paths; they do not replace the hermetic gate.
