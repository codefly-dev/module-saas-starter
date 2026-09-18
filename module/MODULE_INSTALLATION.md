# Bounded module installation

Accounts offers an opt-in, nonhuman installation endpoint that reuses its
existing `InstallSolution` transaction: one server-issued agent principal, one
solution scope node, one standing role grant and one organization installation.
It does not provision workloads, provider credentials, databases, organization
users, roles, or the separate solution routing/frontend registration.

## Bootstrap and authority

An organization administrator first creates/resolves the organization, its
accountable active human owner and the least-privilege role through existing
Accounts APIs. These are server-issued IDs. The organization administrator
approves a delegation identifying the installer, module revision, role and exact
permission envelope. A platform operator projects that approved policy into
Accounts using the protected configuration delivery system. Policy publication
is an explicit trust-root operation; an ordinary module release declaration
cannot write this projection. This first implementation relies on the
operator's reviewed policy-delivery process to establish the organization's
approval; it does not implement an organization-admin policy publication API.

The operator also provisions a dedicated, create-once module identity secret.
Its digest goes into `federation.MODULE_IDENTITY_SECRETS`, and the protected
plaintext goes only to the installation Job. Declare the identity in
`module-capabilities.MODULE_PRINCIPALS`, bound to the approved organization.
It needs no queues, event namespaces, cross-tenant grant or administrator role.
The existing `ModulePrincipalID(prefix)` derives its system identity. This is
separate from the module's application agent, and needs no agent registration
or human API key to bootstrap itself.

`MODULE_INSTALLER_POLICY_FILE` is the absolute path to the policy projection.
Unset disables the HTTP surface. Invalid policy fails startup; subsequent
missing, malformed or oversized projections fail each request closed. The policy
file is limited to 1 MiB, including whitespace. All replicas must
receive the approved projection. Mount the containing directory, so atomic
projection replacement is visible; a pinned subPath mount will not refresh.

The four installer paths (`token`, `inspect`, `apply`, and `verify` under
`/v1/module-installations/`) are served on the private REST endpoint by one
policy-backed handler, including the dedicated module identity exchange and
per-request delegation checks. Without the installer policy, no installer routes
are added.

Example policy (replace the placeholder UUIDs with API-issued references):

```json
{
  "version": "accounts.module-installation-policy/v1",
  "delegations": [{
    "prefix": "example-installer",
    "organizationId": "11111111-1111-4111-8111-111111111111",
    "moduleId": "acme.example/solution",
    "agentIdentifiers": ["acme.example/solution:1.0.0"],
    "solutionIdentifier": "example-solution",
    "roleId": "22222222-2222-4222-8222-222222222222",
    "rolePermissions": ["documents:read"],
    "allowedAudiences": ["example.api"],
    "allowedScopes": ["documents"],
    "ownerPrincipalId": "33333333-3333-4333-8333-333333333333",
    "expiresAt": "2027-01-01T00:00:00Z"
  }]
}
```

The policy has no wildcard organization, module, revision, permission or empty
unrestricted ceiling. The request cannot supply an owner or change delegation.
Role permissions are the exact `resource:action` strings currently present in
Accounts. The API compares them with both the declaration and policy on every
call. A role changed outside this installation therefore fails reconciliation;
this capability does not make roles immutable against a separately authorized
role administrator.

## Executable HTTP contract

Call Accounts' configured private HTTPS listener, or its REST listener over
the existing trusted TLS/mesh transport. The new paths are opt-in raw HTTP handlers; they do not change the
public ORG_ADMIN RPCs or install a gateway route. The installation Job requires
an explicit network/mesh route to these paths, and must verify the Accounts CA.
The handlers validate their own credentials; neither the shared internal token
nor a cloud workload identity alone authorizes installation.

`POST /v1/module-installations/token`:

```json
{"prefix":"example-installer","secret":"<protected dedicated identity secret>"}
```

This calls the existing module identity authorization and Work Context signer,
checks that a live delegation exists for that identity and tenant, verifies the
tenant exists, and durably records issuance before returning the capability.
Response fields are `token`, `expiresAt` (RFC3339), `principalId`, and `tenant`.
The token uses the retained Accounts signing key, configured Work Context
issuer, `module-capabilities` audience and existing maximum 15-minute lifetime
(with the existing verifier's clock-skew allowance). Re-exchange after expiry;
do not commit tokens or copy them into long-lived configuration.

The secret itself is a protected bootstrap identity credential, not a personal
key. It remains stable across repeated installation. Rotate it explicitly by
replacing the protected plaintext and digest and restarting Accounts to load
the digest. To revoke outstanding capabilities immediately at the request
boundary, remove the delegation from the projected policy or expire it.
Revocation takes effect on each replica when its projection is updated. An
already authorized transaction may finish; deleting policy is not cancellation
of an in-flight transaction. Narrowing policy also takes effect per request.

`POST /v1/module-installations/inspect`, `/apply`, or `/verify` takes the signed
capability in `X-Codefly-Work-Context` and the same JSON body:

```json
{
  "moduleId": "acme.example/solution",
  "organizationSlug": "example-org",
  "agentIdentifier": "acme.example/solution:1.0.0",
  "solutionIdentifier": "example-solution",
  "roleId": "22222222-2222-4222-8222-222222222222",
  "expectedRolePermissions": ["documents:read"],
  "allowedAudiences": ["example.api"],
  "allowedScopes": ["documents"],
  "displayName": "Example Solution",
  "rootScopeLabel": "Example Solution"
}
```

Accounts resolves `organizationSlug` through its existing organization store,
then compares the persisted ID with both token tenant and delegation. It
checks the current owner's administrative eligibility and role permission
contents before mutation. Inspect returns `state: absent` or `state: ready` and
`changed: false`; apply returns `state: ready` and reports whether it created
anything. Verify fails on an absent or incompatible installation. Ready responses
include `organizationId`, `principalId`, `installationId`, `scopeNodeId`, and
`grantId`. Absent responses contain only `organizationId` alongside state and
changed. IDs are always read from the server, including after a lost response.

HTTP statuses: 400 invalid shape/unknown fields; 401 missing, expired, wrongly
signed or wrong-audience capability; 403 missing/expired/revoked delegation or
scope mismatch; 409 incompatible existing state, absent verification, missing
approved role or ineligible owner; 503 unavailable policy, issuer, audit or
persistence. Error bodies contain an `error` string and no credential values.
A client must inspect all intended organizations before applying any one of
them, and retry only bounded transient failures. Cross-organization installation
is intentionally not atomic: each authorized organization transaction is.

## Repeatability and lifecycle

Migration 139 adds durable installer ownership to the existing installation
row; its ordinary tenant RLS remains intact. A transaction-scoped PostgreSQL
advisory lock serializes both installer and existing human installation by
organization and solution. Existing uniqueness constraints remain the final
race guard. The installer compares immutable identity, ceiling, owner, role,
lifecycle and every standing grant before returning an existing row. An extra
grant anywhere in the organization fails closed for explicit review. It never
adopts a human installation or a free-standing agent with a matching name.

Creation and the installation audit/outbox are committed in one transaction.
The audit actor is the verified system installer; the approved human remains
the accountable owner and grantor. A no-op request emits no new installation
creation audit. Failure before commit leaves no partial principal/grant/row;
if the commit succeeded but the response was lost, inspect returns the same
IDs and apply is a no-op. There is no request-level secret generation,
provider dispatch or migration execution in this endpoint.

An installed solution identifier has one active immutable agent revision.
Changing that revision, ceilings, role or ownership returns conflict; this
initial installer does not automate an in-place upgrade, ownership transfer,
disable, re-enable or uninstall. Existing admin lifecycle APIs remain separate
reviewed operations. The installer never deletes/recreates an agent to repair a
mismatch or revives a revoked installation. Historical principals, databases,
keys and durable runs are retained. Changing a routing solution identifier merely
to circumvent this conflict is not a supported upgrade procedure. A reviewed
version transition and in-flight runtime qualification remain separate work.

The solution registration secret/token, gateway upstream and frontend halves
remain their existing owner-bound registration flow; the module Work Context
is not a substitute for any of them.

## Verification

From `module/services/accounts/code`:

```sh
go test -race -tags=pure . ./pkg/adapters ./pkg/infra
python3 tools/test_module_installer.py
```

The second command requires Docker, Go, and an already-present
`postgres:16-alpine` image. It creates a randomly named disposable container
bound to loopback, installs this checkout's store ledger, runs the real
PostgreSQL integration cases under the race detector, then removes only that
container. It never selects Kubernetes or cloud credentials.

Tests exercise real retained-key-format Work Context issuance/verification,
wrong secret/issuer/audience/expiry, policy expiry/removal, organization/module/
revision/permission expansion, transaction rollback, simultaneous first apply,
lost successful response, no-op repeat, extra grant drift, suspended owner and
refusal to adopt a human registration. HTTPS handler integration verifies a
single installation audit after repeated application. The optional
`MODULE_INSTALLER_CLIENT_SCRIPT` names a separately reviewed Python client;
the test hands it a temporary scenario containing the ephemeral HTTPS endpoint,
CA and local fixture credentials to qualify a consumer implementation against
this actual API. Those temporary files disappear with the test.

The host tests use the same private-host constructor as normal startup and
validated loopback TLS for all four operations and denial/default dispatch.
Their persistence is a test double; database transaction claims come only from
the separate PostgreSQL tests.

These checks establish local authentication and organization registration
repeatability. They do not establish fresh-cluster deployment, runtime secret
projection, mesh reachability, solution routing/UI enablement, controlled or
paid-provider execution, or signed-in UI-to-runtime acceptance. Those require
a designated isolated target and separate coordinated effects. The full
`codefly ci run` release gate and hosted integration remain mandatory.

## Proposed independent authority reference

The existing request accepts an optional `authorityReferenceVersion` set to
`accounts.module-installation-authority/v1`. Omission preserves the existing
response byte shape (no new authority field). An unsupported version returns
400; it never silently falls back. A successful opted-in `ready` result also
contains:

```json
{
  "authorityReference": {
    "schemaVersion": "accounts.module-installation-authority/v1",
    "digest": "sha256:<64 lowercase hexadecimal characters>"
  }
}
```

Accounts produces this reference only after the existing delegation, tenant,
owner eligibility, exact role/ceiling, installation ownership and health checks
succeed. An absent installation has no reference. A transaction/audit/commit
error returns no result, including when the commit outcome is uncertain; inspect
again using the existing bounded recovery protocol. Generating a reference adds
no registration, grant, audit exemption or authority transition.

This is the identity of the verified installation contract, not a signature,
credential, software release, global authorization revision or promise of future
readiness. Consumers retain their trusted Accounts instance/issuer binding and
continue live authorization; this digest alone grants nothing. Revocation and
current policy checks still happen through the existing runtime mechanisms.

The versioned hash input is one JSON object with these exact keys:

- `schema_version`, set to the requested supported version;
- `module_id`, `organization_slug`, `agent_identifier`, `solution_identifier`,
  from the authorized request;
- `organization_id`, `principal_id`, `installation_id`, `scope_node_id`, `grant_id`,
  from the verified persisted installation;
- `owner_principal_id`, `role_id`, `role_permissions`, `allowed_audiences`,
  `allowed_scopes`, from the matching approved delegation;
- `installer_principal_id`, from the verified caller.

UUIDs are canonical lowercase, hyphenated and nonnil. Permission/audience/scope
sets are nonempty, bounded and duplicate-free, sorted by Go string ordering
(UTF-8 byte order). Object keys are sorted. Encoding is compact UTF-8 JSON with
HTML escaping disabled, no trailing newline, standard JSON control-character
escapes, and escaped U+2028/U+2029. The output is `sha256:` followed by the lowercase
hex digest. The golden test pins the encoded contract independently of the Go
encoder. Consumers use the opaque server result; they do not each reimplement
this calculation. A change to these fields or encoding requires a schema decision.

Software versions, image references, full deployment approval digests, display
labels, installer credential material and delegation expiry are absent. A
successful reinspection after an approved delegation renewal therefore retains
the reference when the installation contract is unchanged. Expired/revoked
policy still refuses inspection. The existing versioned `agentIdentifier` is
an authority selector: ordinary code updates retain it; changing it remains an
explicit approved installation transition. This does not loosen immutable-agent
or existing-installation conflict checks.

Shared installation tooling should carry this reference and the returned
principal ID into versioned, generated consumer configuration/projections. Keep
full desired-software identity on deployment approvals, target checks and
receipts. New consumers require the supported authority response and refuse a
missing or unknown version; old servers reject the new request field. Adopting
this response does not itself migrate an existing consumer, validate software
compatibility or qualify an installed rollout.
