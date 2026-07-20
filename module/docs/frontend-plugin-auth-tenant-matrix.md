# Frontend plugin authentication and tenant matrix

Status: active (`FP-031`, `FP-033`)
Applies to: every backend reached through the generic frontend plugin BFF

This is the canonical security matrix for Warden, Mind, Codefly-owned products,
and future frontend plugins. It separates the guarantees owned by the browser
runtime and SaaS Starter BFF from authorization decisions that only the product
backend can make. A frontend visibility test, fetch mock, or decoded browser JWT
is never evidence that a product endpoint is authorized correctly.

## Authority chain

```text
host auth provider
  -> current opaque access token
  -> public plugin runtime
  -> same-origin SaaS Starter BFF
  -> Codefly-resolved private endpoint
  -> gateway/backend token validation
  -> declared method policy
  -> tenant/resource authorization
  -> tenant-scoped storage
```

The browser runtime owns only retrieval of the current host token and a
same-origin request. The BFF owns transport admission, destination selection,
header stripping, bounds, and safe failure mapping. The gateway or backend owns
signature, issuer, audience, time, revocation, session, impersonation,
delegation, platform-role, tenant-membership, permission, and resource checks.

No layer may reconstruct authority from caller-supplied `x-user-*`, `x-org-*`,
`x-tenant-*`, role, session, forwarding, gateway, internal-token, cookie, URL,
or query metadata. The BFF forwards the bearer unchanged and strips those
values. The backend derives authoritative context from the validated bearer or
from gateway metadata protected by the workspace internal credential.

## Required matrix

Each installed product must exercise the rows below against at least one real
tenant-scoped read operation. Products with mutations must repeat the
permission and resource-substitution rows against one representative mutation.
`Denied` means no product data or mutation; the exact `403` versus concealed
`404` is declared by the product's method/error policy.

| ID | Scenario | BFF obligation | Backend obligation | Required result |
| --- | --- | --- | --- | --- |
| `AT-01` | No bearer, including a forged presence cookie | Reject before target resolution or fetch | Not reached | `401 authentication_required` |
| `AT-02` | Malformed bearer | Reject before target resolution or fetch | Not reached | `401 authentication_required` |
| `AT-03` | Expired, revoked, wrong-issuer, or wrong-audience bearer with valid syntax | Forward the opaque bearer; never decode or repair it | Validate and reject it | `401`; no product data |
| `AT-04` | Valid principal, active tenant, required permission, tenant-owned resource | Forward only the bearer and safe transport headers | Derive principal/tenant and authorize | Success with only active-tenant data |
| `AT-05` | Valid active-tenant member without the method permission or required role | Preserve the backend decision | Enforce declared method policy | `403`; no product data or mutation |
| `AT-06` | Valid principal scoped to organization A supplies organization B directly | Never accept an organization override header | Bind the request organization to validated authority | Denied; no organization B data |
| `AT-07` | Valid principal scoped to organization A supplies a real resource ID owned by B | Forward the path/body only within normal bounds | Resolve ownership and reject substitution before use | Denied, normally concealed `404`; no B data |
| `AT-08` | Platform `support` principal calls a tenant endpoint | Do not infer platform or tenant authority | Allow only when the method policy and delegated/impersonated context explicitly permit it | Default denied; explicitly authorized support operation may succeed |
| `AT-09` | Platform `super_admin` principal calls a tenant endpoint | Do not infer a universal bypass | Apply the product's declared platform-role and tenant policy | Default denied; explicitly authorized platform operation may succeed |
| `AT-10` | Valid impersonation or delegation | Forward only its bearer | Validate actor/subject chain, scope, expiry, and tenant binding | Limited to issued authority; actor remains auditable |
| `AT-11` | Host switches from organization A to B | Read the new token lazily for the next call; send no tenant header | Validate the replacement token and scope results to B | No A data after the switch; no mixed-token request |
| `AT-12` | Caller forges identity, tenant, role, session, forwarding, gateway, or internal headers | Strip every forged value and generate only `x-request-id` | Trust only validated bearer or authenticated gateway context | Decision is identical to the request without forged headers |
| `AT-13` | Capability handshake with a valid bearer | Use the fixed operation and bounded normalized response | Validate authentication, require no product permission, return no tenant/user data | Exact compatible metadata response |
| `AT-14` | Capability handshake with an expired bearer | Convert the backend authentication rejection to a safe host problem | Reject the bearer before returning metadata | `401 authentication_required`, never `backend_incompatible` |

Platform roles are separate from tenant membership. A product may deliberately
define a platform operation for support or super-admin users, but those roles
do not silently turn an ordinary tenant endpoint into a cross-tenant endpoint.
Where a product supports delegated access or impersonation, the backend must
test that bounded path separately; forged browser metadata is never an
acceptable substitute.

## Fixture requirements

A meaningful product run uses real, distinct identifiers rather than missing
records:

- organization A and organization B;
- a same-tenant member with the required permission;
- a same-tenant member without it;
- one resource owned by A and one real resource owned by B;
- expired or revoked access material;
- support and super-admin principals when the product recognizes those roles;
- impersonated or delegated authority when the product supports it.

The product test owns token minting and backend fixtures because only that
repository knows its issuer, method policy, resource vocabulary, and storage
boundary. The SaaS Starter test owns the opaque-token and spoofed-header rows at
the BFF. Both halves are required; a mocked backend decision cannot certify the
product half.

## Protocol and storage proof

Run the product matrix through every browser-facing protocol declared by the
installed service. REST and Connect routes for the same operation must reach the
same authorization policy. A database-backed product must additionally prove
resource substitution using its real tenant-scoped repository or RLS layer, not
only handler mocks.

The capability operation is the sole exception to product permission checks.
It is still authenticated, and its protobuf response contains only schema,
contract, major, and namespaced capability identifiers. It never returns
tenant, user, role, deployment, endpoint, or storage data.

## Evidence and artifacts

Keep machine-readable test output or a concise checked-in matrix that maps each
applicable `AT-*` row to a test name. Live evidence records only scenario ID,
status, and request ID. Never record tokens, cookies, private Codefly endpoint
values, raw tenant data, or internal gateway credentials.

Starter-only unit tests certify BFF behavior. A first-party integration must
also certify the complete browser → BFF → Codefly binding → product backend →
tenant store chain before the supported product configuration can be released.
