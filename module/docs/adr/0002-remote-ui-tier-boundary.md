# ADR 0002: Scope the no-remote-JavaScript rule and place the vetted first-party remote UI tier at the consumer boundary

- Status: Accepted
- Date: 2026-08-16
- Scopes: [ADR 0001](0001-frontend-plugin-packaging.md) (trust model —
  "Loading remote JavaScript at runtime is out of scope")
- Tasks: reserves FP-035, FP-036, FP-045, FP-046 and the isolation backlog for
  a future promotion; no starter task is activated by this decision.

## Context

ADR-0001 states that frontend plugins are "trusted, compile-time dependencies
installed with the application" and that "loading remote JavaScript at runtime
is out of scope." That was the correct decision for the starter's trust model:
the SaaS Starter is a generic template copied by **arbitrary consumers**, so its
default and only shipped mechanism must be one where installing a plugin is a
reviewable build event — a `frontend.config.ts` edit plus a rebuild — and no
consumer inherits a runtime code-loading surface it did not ask for.

A first-party consumer platform now needs the opposite capability for a narrow,
**vetted** case: tier-2 UI panels loaded as Module Federation 2.0 remotes served
from the owning solution's own pod, same-origin, as signed OCI artifacts, with
host-side gating before any remote code is fetched or evaluated. The named
design references are the consumer's `docs/design/ui-mounting.md` and
`solution-delivery.md`. The same shape has precedent outside this project
(Backstage/RHDH BEP-0002: OCI-packaged plugins served as Module Federation
remotes). The adoption-delta context lives in the consumer's tracking issue.

The risk this ADR addresses is **erosion**: without an explicit decision, the
"no remote JavaScript" rule is either quietly violated by a consumer patch under
starter `src/`, or treated as an absolute that blocks a legitimate vetted tier.
Neither is acceptable. This ADR draws the boundary on purpose.

## Decision

### Tiers of trust

We name three tiers so the rule can be scoped precisely instead of relaxed
wholesale:

- **Tier 1 — trusted compile-time plugins.** The only mechanism the starter
  SDK ships. Contributions are npm workspaces resolved at build time; install is
  a `frontend.config.ts` edit plus a rebuild. ADR-0001 governs this tier in full
  and is unchanged.
- **Tier 2 — vetted first-party remote UI.** Module Federation 2.0 remotes,
  same-origin, signed, host-gated. Legitimate for first-party solutions that own
  their own pod and vet their own artifacts.
- **Tier 3 — untrusted remote UI.** Remotes from parties the host does not vet.
  Requires iframe/worker isolation. **Reserved and explicitly out of scope**; no
  part of this platform loads untrusted remote code today.

### The scope of ADR-0001's rule

ADR-0001's "loading remote JavaScript at runtime is out of scope" is **scoped to
Tier 1**, the starter SDK's default trust tier. It remains absolute there. It is
not a claim that a vetted first-party Tier 2 can never exist — only that the
generic starter SDK does not itself provide one.

### Where Tier 2 lives

The vetted first-party Module Federation tier is a **consumer-platform
capability, not a starter-SDK primitive.** The starter does not gain a Module
Federation host, a remote loader, an OCI fetch/verify path, or a remote-manifest
handshake. A consumer that needs Tier 2 builds it in its own platform, on top of
the stable seam the starter already provides and continues to provide unchanged:

- **Named-slot registration is kept.** Tier 2 panels register into the same
  named route/widget/tile slots as Tier 1 contributions. The host's contribution
  model does not fork.
- **Same-origin serving.** Remotes are served from the solution's own pod over
  the host's existing same-origin browser transport; no cross-origin code fetch
  and no public product URL (consistent with ADR-0001 and FP-036).
- **Host-side gating before load.** The host's one presentation evaluator
  (routes, navigation, widgets, commands, tiles) and the protobuf backend
  capability handshake gate a Tier 2 panel *before* its remote entry is fetched
  or evaluated — gating precedes code loading, never follows it.
- **Page-reload on install, no live splice.** Installing or enabling a Tier 2
  panel takes effect on a page reload. The host never live-splices a new remote
  into a running React tree.
- **No branding or arbitrary CSS from remotes.** Branding and appearance remain
  application data per ADR-0001; a remote panel cannot contribute either.

Signed OCI artifact production and verification, remote-manifest resolution, and
the pod that serves the remote are all owned by the consumer platform and its
delivery pipeline, not by the starter.

### Promotion path (if Tier 2 is ever pulled into the SDK)

Should a first-party Tier 2 be promoted from a consumer capability into a
starter-SDK primitive later, that is a **new ADR that supersedes this decision**,
gated on — and sequenced after — the reserved roadmap items, not merged ahead of
them:

1. Namespaced semantic permissions applied consistently to every surface and
   action (FP-045, FP-046), so a remote panel is gated by policy the host owns.
2. Transport observability and retirement of any direct product URLs (FP-035,
   FP-036).
3. New SDK work not yet in the backlog: signed-artifact verification at the
   host boundary and a remote-manifest compatibility handshake analogous to the
   existing `{contract, major}` backend handshake.

Tier 3 (untrusted remotes, iframe/worker isolation) stays reserved behind Tier 2
and is not sequenced by this ADR.

## Consequences

- The starter's trust model is unchanged: a clean starter still runs with no
  product plugin, and Tier 1 remains its only shipped install mechanism. No new
  runtime code-loading surface reaches arbitrary consumers.
- A first-party consumer may build a vetted Tier 2 Module Federation tier in its
  own platform without patching starter `src/`, because the host seam it needs —
  named slots, same-origin transport, pre-load presentation gating, page-reload
  install — is already public and stable.
- The "no remote JavaScript" rule is now explicit rather than absolute: it binds
  Tier 1 and does not silently forbid a vetted first-party Tier 2. A consumer
  patch that loads remote code under starter `src/` is still a boundary
  violation; the sanctioned place for Tier 2 is the consumer platform.
- Promoting Tier 2 into the SDK, or opening Tier 3, each requires its own ADR and
  the sequenced prerequisites above; neither is authorized here.
