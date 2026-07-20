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
