# Frontend plugin architecture

The SaaS starter is a generic host application. It supplies identity-aware
shells, extension outlets, composition, presentation policy, and server-owned
transport. A product package supplies its domain UI and backend requirements.
Warden is the first reference consumer, not the platform shape.

## Ownership

```text
SaaS public packages
├── @codefly/saas-plugin-contract  React-free metadata + pure composition
├── @codefly/saas-plugin-react     registration + runtime + isolation boundary
└── @codefly/saas-plugin-testkit   reserved certification helpers

SaaS frontend host
├── src/                           private implementation
├── frontend.config.ts             branding, appearance preset + explicit installs
└── server/                        server-only endpoint resolution

Product frontend package
├── manifest                       JSON-safe routes, navigation, widgets, requirements
├── react registration             lazy components bound to manifest IDs
├── model                          DTOs, generated clients, repositories
├── controllers                    queries, mutations, cache policy
├── views                          product routes and widgets
└── tests                          domain + public-boundary conformance
```

Product packages never import the host's auth store, feature controllers, page
tree, transports, providers, query keys, or `@/` alias. If a reusable seam is
needed, it becomes a separately reviewed public SDK primitive.

## Compile-time and runtime flow

```text
installed package export
  -> frontend.config.ts
    -> defineReactFrontend
      -> defineFrontend metadata validation
        -> immutable JSON-safe application inventory
      -> exact route/widget component registration
        -> generic host adapters and build tooling

host contribution outlet
  -> host capability gate for every declared service
    -> fixed REST well-known path or generated Connect procedure
      -> strict ProtoJSON contract/major comparison

product view
  -> usePluginService(plugin, alias)
    -> product controller/repository
      -> closure-backed host transport
        -> same-origin /api/plugins/{plugin}/{alias}/{relative-path}
        -> generated service allowlist
          -> generated frontend Codefly service dependency
            -> server-resolved Codefly binding
              -> backend bearer validation + tenant authorization
```

Plugins are bundled at build time. Backend availability and API compatibility
are runtime facts represented by contained, observable states.
Demo or fixture repositories are explicit configuration; a live failure never
silently falls back to sample data.

## Contract invariants

- Plugin names and JSON-safe contributions are validated without importing
  React; component registrations are validated separately before rendering.
- Every declared route/widget ID has exactly one lazy React component. Missing,
  duplicate, and extra registrations fail composition.
- Route, navigation, and widget collisions fail composition.
- Service requirements contain logical aliases and API metadata, never URLs or
  credentials.
- Application bindings contain only plugin/alias and logical module/service
  identifiers; protocol endpoints are derived from validated requirements.
- Missing, extra, duplicate, or unsafe bindings fail deterministic allowlist
  generation before the host starts.
- External Codefly service dependencies are generated from that allowlist;
  product installs never edit the frontend service manifest inline.
- The protected root npm manifest exposes one `packages/*` seam. Product
  workspaces and lock/service projections are additive or generated, while
  Docker, base integrity, and build gates verify their convergence.
- The injected runtime exposes service lookup, not the host token accessor or a
  configurable destination; it refreshes the bearer privately on every call.
- Failed BFF responses map to a safe public descriptor with only state, stable
  code, optional validated request ID, and optional bounded retry delay.
- The protobuf-defined capability handshake is the only backend runtime
  compatibility authority. The BFF probes a fixed REST/Connect operation,
  validates strict schema version `1`, and compares the response with the
  installed service contract/major before a contribution renders. Successful
  probes are runtime-cached; failures remain retryable.
- Every route and widget owns a Suspense/error boundary. Loading is local,
  normal rendering is ready, typed failures become unavailable, incompatible,
  or failed, and unknown render exceptions become `render_failed` without
  exposing the original message or stack.
- The BFF strips caller identity, tenant, cookie, forwarding, and hop-by-hop
  headers; downstream services derive authority only from a validated bearer.
- The canonical
  [authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md)
  makes this split executable: Starter certifies opaque-token/header behavior;
  each product certifies claims, method policy, tenant/resource ownership, and
  storage isolation with two real tenants.
- Each BFF attempt owns a fresh `x-request-id`. Caller/backend identifiers
  cannot override it, and only the host response header may enter the public
  failure descriptor. W3C trace propagation remains separate and untrusted.
- The protocol fixes the method set, and the installed route prefix fixes the
  reachable upstream namespace.
- Only implemented host protocols and extension outlets enter the public API.
- Branding and the semantic light/dark appearance preset belong to the
  application, not a product side effect. Plugins render semantic tokens only.
  The preset covers per-mode color plus shared structural/typographic tokens
  (radius, fonts, `fontSizeBase`, `spacing` density, sidebar widths,
  `borderWidth`, `shadowStrength`) — enough for a full compose-time reskin.
- An organization may overlay its validated HTTPS logo/favicon and six-digit
  primary color at runtime; it cannot contribute arbitrary CSS or change plugin
  composition.
- UI visibility is defense in depth; backend authorization remains mandatory.
- Starter-only operation is always supported and continuously tested.

## Source-of-truth rule

The ADR, public import map, contract package, tests, and platform TODO in this
repository are authoritative. Consumer repositories may document their own
package extraction and integration status, but conflicting host architecture is
resolved here.

The complete request, header, limit, failure, and trust semantics are frozen in
[the BFF contract](frontend-plugin-bff-contract.md).
The required host and product authorization evidence is frozen in the
[authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md).
The public diagnostic identifier lifecycle is frozen in the
[request-correlation contract](frontend-plugin-request-correlation.md).
The exact application lifecycle is frozen in the
[install and uninstall procedure](frontend-plugin-installation.md).
