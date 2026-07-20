# Declarative RPC method policy

`saas.policy.v1.MethodPolicy` is the finite contract consumed by Codefly's
descriptor compiler. It lives at
`services/accounts/proto/saas/policy/v1/options.proto` and extends
`google.protobuf.MethodOptions` as field `51000` (`method_policy`).

The schema describes routing and the checks common infrastructure can enforce.
It is not a general policy language. State-dependent rules—last-owner guards,
subscription state, delegated authority, resource lifecycle, and similar domain
logic—remain in tested business code.

## Required authoring rules

Every application RPC must eventually declare exactly one method policy. The
compiler rejects an omitted or `UNSPECIFIED` value; zero values never imply
public access.

- `exposure` is `PUBLIC`, `AUTHENTICATED`, or `INTERNAL`. Public procedures
  cannot declare tenant, permission, scope, or MFA requirements. Internal
  procedures require the workspace internal credential and are never emitted
  into public gateway catalogs.
- `tenant` is the minimum relationship required after identity validation.
  Resource bindings then prove that request identifiers refer to that tenant.
- `permissions` use canonical `resource:action` values. `scopes` are the API-key
  ceilings accepted for the same method. Unknown vocabulary fails generation.
- Each `resource_binding` names a protobuf field path, target kind, and one of
  the finite lookup operations. Arbitrary SQL, expressions, and handler names
  are forbidden in descriptors.
- `mfa` is explicit. `RECENT_STEP_UP` uses the centrally configured freshness
  window. `IF_ENROLLED_RECENT_STEP_UP` preserves the starter's opt-in factor
  policy without claiming that every caller must already be enrolled; handlers
  may demand stronger domain checks but may not weaken the declared floor.
- `platform_role` is separate from tenant membership and records the minimum
  cross-tenant operator role (`ANY`, `SUPPORT`, `BILLING`, or `SUPER_ADMIN`).
  Public and internal procedures must declare `NONE`.
- Audit events are stable dotted identifiers. If emission is not `NONE`, at
  least one event must be present; multi-event and outcome-dependent methods
  list every possible event, and generated metadata records the selected
  success/failure outcomes.
- Mutating procedures declare whether idempotency is forbidden, optional, or
  required. A required key is validated before domain execution.
- `rate_limit` selects a centrally configured budget. Procedures never embed
  numeric limits in protobuf.
- `authentication_factor_attempt` is set only on public login-factor
  verification methods that use the dedicated per-client-IP budget. The
  compiler rejects incompatible exposure, rate class, or request sensitivity;
  gateways must not infer this classification from URL strings.
- Request and response sensitivity independently control log body capture,
  trace attributes, generated examples, and support-tool visibility. `SECRET`
  payloads are never captured.

## Example

```proto
import "saas/policy/v1/options.proto";

rpc RotateSecret(RotateWebhookSecretRequest)
    returns (RotateWebhookSecretResponse) {
  option (saas.policy.v1.method_policy) = {
    exposure: EXPOSURE_AUTHENTICATED
    tenant: TENANT_REQUIREMENT_ORG_ADMIN
    permissions: "webhooks:write"
    scopes: "webhooks:write"
    resource_bindings: {
      request_field: "id"
      target: RESOURCE_TARGET_OWNED_RESOURCE
      lookup: RESOURCE_LOOKUP_RESOURCE_TO_ORGANIZATION
    }
    mfa: MFA_REQUIREMENT_RECENT_STEP_UP
    audit: {
      events: "webhook.secret_rotated"
      emission: AUDIT_EMISSION_SUCCESS_AND_FAILURE
    }
    idempotency: IDEMPOTENCY_REQUIREMENT_REQUIRED
    rate_limit: RATE_LIMIT_CLASS_SENSITIVE
    request_sensitivity: SENSITIVITY_SECRET
    response_sensitivity: SENSITIVITY_SECRET
  };
}
```

All accounts RPCs now carry these options. Descriptor-derived policy is
authoritative for runtime admission, introspection, `AUTHZ_MATRIX.md`, and the
normalized `generated/service-catalog.json` and
`generated/authz-methods.json`; a method with a missing or invalid policy is
deliberately absent from the runtime
lookup and therefore denied. Only prose descriptions remain handwritten until
P1-DOC-001 source-comment extraction replaces the editorial description map.
