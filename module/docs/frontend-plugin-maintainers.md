# Frontend plugin ownership and review policy

Date: 2026-07-16
Status: canonical ownership policy
Task: FP-011

The SaaS Starter owns the generic host and public SDK. Each product repository
owns its compile-time product package. Ownership follows source responsibility;
installing a product does not transfer its domain code to the Starter team.

## Current maintainers

| Surface | Repository/path | Maintainer | Required responsibility |
| --- | --- | --- | --- |
| Contract and composition | `module-saas-starter/module/services/frontend/code/packages/saas-plugin-contract` | Starter maintainers | Contract major, SemVer, validation, public export map, migration impact. |
| React composition/runtime | `module-saas-starter/module/services/frontend/code/packages/saas-plugin-react` | Starter maintainers | Component-registration integrity, React peer line, injected runtime safety, public hooks, package contents. |
| Generic host boundary | Starter composition, plugin BFF, service dependency compiler, and canonical frontend-plugin docs | Starter maintainers | Host isolation, auth/tenant transport, install lifecycle, consumer-neutral behavior. |
| First-party consumer package | The consuming solution's own frontend plugin package, in that consumer's repository | The consumer's product team | Consumer domain contract, generated client, repository/controller/UI, backend compatibility, product tests. |

The named accounts behind these rows live in `.github/CODEOWNERS`, which is the
enforcing surface; this table names surfaces and responsibilities, not people.
A consumer integration is not complete until that consumer's repository protects
its product package path in its own `.github/CODEOWNERS`; that consumer-side
change belongs to FP-005/FP-010 and must not be simulated by a Starter rule.

Planned packages such as `@codefly/saas-plugin-testkit` remain unpublished and
have no active ownership surface. Activation requires a named maintainer and a
CODEOWNERS entry in the same change.

## Review rules

- Any public export, entrypoint, contract type, compatibility rule, or package
  version change requires the contract maintainer.
- Host implementation changes that do not alter the public contract require
  the generic host maintainer and must retain consumer-style boundary tests.
- A product package change requires its product owner. If it also changes the
  Starter contract or runtime, split the changes into independently reviewable
  repository commits and require both owners.
- Generated files do not define ownership. Review follows their handwritten
  source: application config, product manifest, topology, schema, or generator.
- A maintainer change updates this document and the owning repository's
  CODEOWNERS together. Do not leave an active package without a reviewer.

## Release responsibility

The public-package maintainer records the package version, exact exports,
supported framework range, package contents, tests, and migration notes. A
product owner records the exact SDK version and backend compatibility major it
has certified. Neither side may declare compatibility on behalf of the other.
