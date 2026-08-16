# Frontend plugin convergence brief

Date: 2026-07-15
Authority: canonical SaaS starter frontend-plugin direction

This repository is the source of truth for the generic host and public SDK.
Product repositories such as Warden, Mind, or Codefly-owned applications are
consumers and proving grounds; they do not define private exceptions to this
contract.

The Codefly `nextjs` service agent owns only the framework/build substrate. It
must support additive npm workspaces and reproducible non-root builds, but it
does not define `FrontendPlugin`, discover product packages, or duplicate this
repository's composition/runtime policy.

Read before changing the plugin boundary:

1. [Architecture](frontend-plugin-architecture.md)
2. [Implementation plan](frontend-plugin-platform-implementation-plan.md)
3. [Execution TODO](frontend-plugin-platform-todo.md)
4. [Packaging ADR](adr/0001-frontend-plugin-packaging.md)
5. [Public import map](frontend-plugin-public-api.md)
6. [Same-origin BFF contract](frontend-plugin-bff-contract.md)
7. [Authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md)
8. [Request correlation](frontend-plugin-request-correlation.md)
9. [Install and uninstall procedure](frontend-plugin-installation.md)
10. [Ownership and review policy](frontend-plugin-maintainers.md)

## One direction

1. `services/frontend/code/frontend.config.ts` is the only application-owned
   composition root and owns branding, the semantic appearance preset, and the
   explicit installed-plugin list.
2. `FrontendPlugin` is the React-free, JSON-safe metadata contract.
   `defineReactPlugin` binds lazy components by stable route/widget ID; there is
   no `AdminPlugin` alias or host-private compatibility barrel.
3. `AdminLayout` is the only mounted shell and every adapter consumes injected
   `FrontendConfig`.
4. Product packages import only active `@codefly/saas-plugin-*` entry points.
   Starter `src/` paths are private.
5. `frontend.config.ts` also maps installed service aliases to logical Codefly
   module/service targets. Generated routing input derives the protocol and
   cannot contain URLs, credentials, or endpoint overrides.
6. Browser calls are same-origin. Deployment URLs and Codefly binding
   resolution are server-only host concerns. Products obtain the injected
   transport from `@codefly/saas-plugin-react`; they do not import host auth or
   construct a parallel client.
7. The generated server allowlist also generates the frontend's external
   Codefly service dependencies. Product installs never patch the service
   manifest directly.
8. Products own their manifests, models, repositories, controllers, views,
   generated clients, fixtures, and domain tests.
9. Backend authorization is authoritative. Presentation access is evaluated by
   one host policy across routes, navigation, widgets, commands, and tiles.
10. Plugins are trusted compile-time dependencies. Runtime remote JavaScript is
   not part of the starter SDK; ADR-0002 scopes this to the trusted compile-time
   tier and places a vetted first-party Module Federation tier at the consumer
   boundary.
11. The generic Next.js service agent supplies the workspace-capable Next.js
   substrate. SaaS Starter remains the only source of truth for the SaaS plugin
   SDK, host shell, composition, BFF, and product installation contract.
12. Backend compatibility is a protobuf-defined fixed REST/Connect handshake.
   The host compares its strict normalized response with the installed
   contract/major before rendering; products do not invent alternate health or
   version probes.

## Change rules

- Claim TODO IDs and owned paths before editing.
- Preserve unrelated dirty-worktree changes.
- Change a canonical decision through the ADR and public import map first.
- Do not add product names, DTOs, endpoints, environment variables, scripts, or
  conditionals to generic starter source.
- Do not publish a private host module to unblock one consumer. Add only the
  smallest product-neutral primitive with host and consumer-style tests.
- Add a manifest field only with validation, a host consumer or inventory, and
  a conformance test.
- Keep application installation to one additive product workspace plus one
  composition-root registration; generated lock, allowlist, and service
  dependency outputs follow mechanically. Removal is the inverse operation.

## Ownership lanes

| Lane | Owns |
| --- | --- |
| Contract/composition | JSON-safe public types, pure validation, package exports |
| React composition | exact component registration and application composition |
| Host adapters | providers, shell, outlets, presentation policy, shared runtime |
| Host transport | service inventory, allowlist, same-origin BFF, Codefly bindings |
| Product package | product manifest, domain MVC, generated clients, fixtures, package tests |

## Handoff format

```text
Task IDs:
Canonical decisions changed: no | yes (ADR link)
Owned files changed:
Behavior delivered:
Tests/checks run and results:
Generated artifacts changed:
Remaining risks/blockers:
Suggested next task IDs:
```
