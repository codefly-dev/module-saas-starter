# Supply-chain security

Supply-chain validation is part of the canonical `codefly ci run` gate. There
is no separate repository-authored security pipeline.

Codefly and the resolved service plugins own dependency auditing, vulnerability
policy, deployable artifact construction, and CycloneDX SBOM generation. Their
typed results are recorded in `.codefly/ci/report.json`, with content hashes for
the returned evidence. Provider workflows only invoke Codefly; they do not
install scanners, duplicate container builds, or maintain language-specific
security matrices.

Repository policy inputs such as `.gitleaks.toml`, `.gitleaks-baseline.json`,
and `.trivyignore.yaml` remain declarative inputs for compatible generic plugin
checks. Exceptions must be path- or package-scoped, justified, time-bounded,
and reviewed. Never commit an unredacted scanner report or a machine-local CI
artifact.

When a security capability is absent, add it to the universal Core contract and
implement it in the applicable generic plugin. Do not compensate with bespoke
GitHub Actions steps in the SaaS Starter.

## Known agent-image findings tracked upstream

Some `codefly ci run` / `codefly audit workspace` findings originate in a
resolved **service agent's container image** (its base OS packages or the Go
modules compiled into the agent binary), not in this repository's own
`go.mod`/`package.json` sources. This module pins each service's agent version in
`module/deployment/topology.bindings.codefly.yaml`; when the pinned agent is
already the latest published release, such a finding cannot be remediated here by
a pin bump and must be fixed upstream in the agent's own repository, after which
the pin is raised.

Currently tracked (agents already at their latest published versions):

- **`vault`** (vault agent `0.0.27`) — CVE-2026-56854 (CRITICAL):
  `golang.org/x/crypto` `v0.53.0`, fixed in `0.55.0`; CVE-2026-84304 (HIGH):
  `google.golang.org/grpc` `v1.82.1`, fixed in `1.83.1`. Upstream:
  codefly-dev/service-vault#42.

Resolved upstream and now remediated here by a pin bump:

- **`store`** (postgres agent) — CVE-2026-14456 (HIGH), Alpine
  `libcrypto3`/`libssl3` `3.5.7-r0` → `3.5.8-r0`. Fixed upstream in
  codefly-dev/service-postgres#74 and shipped as postgres agent `0.0.130`; the
  `store` pin is raised to `0.0.130` in this repo.

When an upstream release ships, bump the pin (see AGENTS.md "Agent version
pins"), verify the graph still boots (`codefly run service`), refresh the base
manifest, and re-run `codefly audit workspace` to confirm the finding clears.

## Immutable module releases

`module/module.package.codefly.yaml` is the composition-v2 package contract.
After the canonical Codefly gate passes for a matching `v<package-version>` tag,
the release job builds `module.tar` directly from the clean tagged Git tree. The
archive has a stable path order and metadata because Git produces it from one
exact commit rather than from a mutable working directory. The job builds it
twice in CI and compares the archive, checksum, and artifact metadata byte for
byte.

Each GitHub release contains the canonical archive, `module.tar.sha256`,
`artifact.json`, Core's `codefly/module-provenance/v2` document and detached
Ed25519 signature, a CycloneDX package SBOM, and the supplemental Sigstore
bundle for GitHub's signed build-provenance attestation. Core verifies the
artifact digest, repository, tag, commit, package identity, and configured
signer before materializing the archive. Publication fails when the repository's
immutable-release setting is disabled, the release already exists, the tag does
not match the manifest version, the tagged commit is not reachable from
`main`, or the remote tag peels to a different commit.
Once published, GitHub prevents replacement or deletion of the tag and assets.

The repository secret `RELEASE_ADMIN_TOKEN` must contain a fine-grained token
with read-only repository Administration permission. GitHub's workflow token
cannot be granted that permission, so this credential is used only to read the
immutable-release setting; publication uses the job-scoped workflow token.

`RELEASE_PROVENANCE_PRIVATE_KEY` must contain the base64 encoding of an Ed25519
seed (32 bytes) or private key (64 bytes). Its public key must be registered in
the Core trust policy for the signer identity
`https://github.com/codefly-dev/module-saas-starter/.github/workflows/ci.yml@refs/heads/main`.
The release job reads the secret through standard input, signs the exact
persisted provenance bytes, and fails before creating a GitHub release when the
secret is missing or invalid.

`RELEASE_PROVENANCE_PUBLIC_KEY` contains the base64 32-byte public key from that
same Core trust-policy entry. The publisher requires the private key to match it
and verifies the newly created signature with it before any release asset is
uploaded.

Verify a downloaded package before materialization:

```sh
sha256sum --check module.tar.sha256
gh attestation verify module.tar --repo codefly-dev/module-saas-starter
```

Core consumers additionally verify `provenance.json` and `provenance.sig`
through their configured repository and signer trust policy before extraction.
