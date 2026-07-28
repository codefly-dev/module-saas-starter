# Trust capability and evidence model

`services/frontend/code/src/features/trust/capability-manifest.json` is the
machine-readable source for the starter's security, privacy, recovery, legal,
and assurance claims. The `/docs/compliance` readiness view consumes it
directly.

The manifest separates five states:

1. `absent`
2. `implemented`
3. `configured`
4. `operationally_verified`
5. `externally_attested`

`designState` records only what is present in starter source. Provider and
setting requirements describe deployed configuration. Neither source nor
configuration is production evidence by itself.

Every evidence record is bound to one capability, environment, and scope. It
also records its owner, verifier, private source or artifact reference,
performed time, review time, optional expiry, status, and the highest state it
supports. Evidence from another environment or scope cannot promote a claim.
Expired, revoked, rejected, or review-overdue evidence is ignored.

Evidence sources are private by default. A record may expose a separate
`publicSummary`; the public projection never includes its source, owner, or
verifier. Legal and security owners must review even a safe summary before
adopters use it in customer-facing material.

## Responsibilities

- `starter` means source supplied by this module.
- `provider` means behavior supplied by hosting or another service provider.
- `adopter` means policy, legal, operational, or deployment work owned by the
  company using the starter.
- `shared` means a starter control still requires provider or adopter work.

The unconfigured distribution intentionally carries no production evidence.
For example, deployment manifests can support TLS, storage, backups, or an
external status provider, but those capabilities remain unavailable as public
claims until the named environment has current evidence.

## Release gate

`node tools/base-integrity.mjs check` validates the manifest and scans public
starter documentation and frontend source for unsupported fixed claims. The
same validation runs before regenerating the base manifest, so canonical and
composed-module release paths fail together.

When adding a public claim:

1. add or update the capability instead of hard-coding deployment behavior;
2. declare starter, provider, adopter, or shared responsibility;
3. choose the minimum state required to render the summary;
4. add environment-scoped evidence only after the control is exercised or
   externally attested; and
5. verify that removing configuration or expiring evidence downgrades the
   readiness view.

The manifest is a claim gate, not a certification, legal agreement, security
assessment, recovery exercise, or substitute for adopter review.
