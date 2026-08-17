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
