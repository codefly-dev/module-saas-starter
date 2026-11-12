# Host configuration groups

Each environment declares values in `<group>.env` and secrets in
`<group>.secret.env`. Codefly delivers these to services declaring the group;
consuming workspaces override a group rather than editing module-owned files.
A document group is one YAML document, `<group>.yaml`: `fixture-grants.yaml`
holds the roles a composing workspace's people hold at the modules it composes,
added to the selected fixture (`services/accounts/code/fixtures/workspace_grants.go`);
the default grants nothing.

### Fixture grant identities

Accounts validates the complete selected fixture plus its workspace grants before
the first seed write. Both identical and conflicting duplicate identities are
refused, with the two row indices; rows are never unioned or deduplicated:

- roles: `(org, name)`;
- assignments: `(org, role, subject lookup kind, subject identity, scope)`, with
  separate email and agent-identifier lookup namespaces;
- installations: `(org, target)`, regardless of owner, role or team differences.
  A row names `org`, `target` and `owner`; `role` only where the target holds
  one (a module declaring `installation_role: none`, or no role at all, is
  installed holding none), and `teams` only beside a `role`, since a team is
  granted that role.

A role-gated target (a solution, or a module page declaring roles) is installed
as an administrator installs it in the admin area: a solution through its
reviewed install, a module page by composition and then its Set up, each
declared role going to its `<target>-<role>` team (the first team `teams` names
for `role`, if any). `installer_roles` are the roles the owner takes, joining
their teams; `members` maps a declared role to the people, each a member of the
organisation, who join that role's team, as an administrator adds a person to a
team:

A row whose `target` is `"*"` stands for every role-gated target the
composition delivers, whatever the composition is: the host expands it at each
delivery pass into one row per such target no row names, with the `members`
of the roles that target declares, and nothing else (no `role`, `teams` or
`installer_roles`, which are one target's). The module's `dev-admin` fixture
carries one, so its five people hold the same teams on every composition
without the workspace naming a target; a workspace row naming a target
explicitly is kept and that target is not expanded.

```yaml
installations:
  - org: Acme Corp
    target: example-solution
    owner: alex@acme.test
    installer_roles: [installer]
    members: {editor: [maya@acme.test], viewer: [remi@acme.test]}
```

A person holds what their teams are granted on the installation and nothing
else; a fixture role carrying a module's permissions directly gives a person
what no deployed organisation gives anyone.

These keys retain the seed's exact spelling, without case folding or trimming.
The same role name in another organization and distinct assignment subjects or
scopes remain separate and subject to the other validation and seed rules.
Each exact identity must occur once in the complete input. Repeating a valid
invocation retains the existing reuse and permission-mismatch rules.
Composition extensions append imported rows before the consuming workspace's
rows; the resulting document is public policy, not an identity/credential fixture.
An imported workspace extension can be delivered as composition-root configuration
to services beyond Accounts; receiving it does not execute the fixture seed.
Validation does not make seeding transactional: later reference, storage or
installation failures can leave partial effects. Installation convergence collects
errors and continues other pending rows.

## A key a group does not declare is inert

A composition can supply a value for a **declared** key. It cannot declare one.
So a key a service reads and no group declares never reaches the process on any
deployment — and the read returns nothing while the feature reads as wired.

That failure is invisible exactly where it matters. Locally the value is usually
absent anyway, or a stray process variable satisfies the fallback, so the service
behaves. In a cell the operator sets the value, the platform carries nothing, the
service keeps its default, and there is no error to find. `security`'s
`TRUSTED_PROXY_CIDRS` was undeclared for as long as accounts had per-IP rate
limiting, so every deployment attributed anonymous traffic to its proxy's own
address and an operator could not change that. `internal-auth`'s
`JWT_PREVIOUS_PUBLIC_KEYS` was undeclared while `../KEY_ROTATION.md` instructed
operators to set it during a signing-key rotation — turning the one mechanism
that makes such a rotation survivable into the thing that stops every
outstanding credential verifying.

`module/tools/workspace_configuration_dependencies_test.go` holds both halves:
every group a service reads is declared in its manifest, and every literal key
it reads is declared in that group's default here. It walks every service's
Go and TypeScript source, so a service cannot pass by not being listed. Its
`keysNoGroupCanDeclare` list names the few that stay inert and why; the list
shrinks and never grows.

The gate sees a read whose group is a literal at the accessor
(`workspaceEnv`, `WorkspaceValue`/`WorkspaceSecret`, sdk-js's
`getWorkspaceConfiguration`/`getWorkspaceSecret`/`getWorkspaceValue`, and the
frontend's `read(group, key)` reader). A group named through a constant or a
group-bound helper (`applicationEnv("KEY")`) is outside its sight: keep such a
read beside its declaration, and prefer the literal spelling.

## A group a service reads but does not declare is undelivered

The same failure one level up: a group is delivered only to the services that
declare it. The frontend read `application`/`APP_BASE_URL` without declaring
`application`, so on a cell it never had the configured origin and presented its
own cluster-internal address to accounts instead, which refused every sign-in
with "invalid trusted public origin". The gate above now covers the frontend.

## application

The deployment's own public facts. Not a credential group: its blast radius is
the three services below, all of which need the origin.

| Key | Read by | Empty means |
| --- | --- | --- |
| `APP_BASE_URL` | accounts (links it mints, the OAuth issuer, the origin it compares a forwarded one with), auth-gateway (a solution's MCP protected-resource metadata and token audience), frontend (the verified public origin it presents to accounts and judges the solution proxy against) | local only: the frontend uses its loopback own endpoint and accounts trusts the forwarded origin. Outside local, accounts refuses to start and a deployed frontend refuses sign-in, naming the key |
| `BOOTSTRAP_ADMIN_EMAIL` | accounts | no one-time bootstrap administrator |
| `ORGANIZATION_CREATION` | accounts | `open`; `platform_admin` or `disabled` restrict it, anything else refuses the boot |

A deployed frontend never stands its own endpoint in for `APP_BASE_URL` when
that endpoint is loopback, cluster-internal (`*.svc`, `*.cluster.local`, a
single-label name) or plain http: it logs what it found and that it expected
this key, and refuses rather than presenting an origin accounts will refuse.

## audit-archive

Where the daily audit archive is written. Not a credential group: the bucket's
credentials belong to the composed object-storage service, which is also the
reason this group names no bucket.

| Key | Empty means | Set to |
| --- | --- | --- |
| `AUDIT_ARCHIVE_URI` | no archive: nothing is scheduled, no worker runs, and the audit log's only copy is the database — which the boot says out loud | a key **prefix** inside the composed object-storage service's own bucket (`audit/archive`). A value naming a bucket, a host or a scheme (`s3://…`) fails the boot rather than writing somewhere the operator did not choose; so does a prefix with no object-storage service composed |

Each complete UTC day becomes one object under that prefix, with a receipt in
the audit log carrying the object key, its digest and the byte and event counts.
The write is create-if-absent, so an archived day's bytes cannot be replaced by
a later run — which makes the archive tamper-**evident**, not immutable. The
object-lock policy that would make it immutable belongs to the infrastructure
that provisions the bucket; [../../AUTHZ.md](../../AUTHZ.md) states exactly what
is and is not proven.

## security

Session, MFA and WebAuthn policy, plus three operator switches whose empty value
is the shipped behaviour. Every one of the three is empty-means-strict: the
shipped behaviour is the stricter reading, and a deployment reaches the weaker
one only by naming it.

| Key | Empty means | Set to |
| --- | --- | --- |
| `TRUSTED_PROXY_CIDRS` | no forwarding header is trusted, so every per-IP budget buckets on the peer — behind an ingress and a frontend hop, one bucket for the whole cell | the cell's ingress and frontend ranges, comma-separated exact IPs or CIDRs. A malformed entry fails the boot rather than trusting a spoofable header. Distinct from the identically named key in the `gateway` group, which is auth-gateway's own edge and which a deployed gateway refuses to start without |
| `ACCOUNTS_REVOCATION_FAIL_OPEN` | a token whose revocation status cannot be read is denied — fail **closed** | `true`, only when fronting accounts directly with no auth-gateway, to keep the direct verify path serving through a revocation-store outage at the cost of a revoked token's remaining TTL |
| `RBAC_CENTRAL_ENFORCEMENT` | `enforce`: request-time central RBAC admission hard-denies a call whose declared org-tenant floor the caller does not satisfy | `shadow` to classify only and deny nothing — an explicit opt-out the boot warns about by name, every boot. Any other value fails the boot, so a typo cannot leave enforcement off |

## internal-transport

The mesh assertion and the egress it carries. The composed modules read the
same group, so a cell asserts the mesh once.

| Key | Empty means | Set to |
| --- | --- | --- |
| `mesh-protected` | `false`: plaintext to a routable in-cluster address is refused | `true` only when every in-cluster hop is carried by a mutually authenticated mesh. Any value other than `true` or `false` fails the boot |
| `mesh-egress-range` | `240.240.0.0/16` (Istio ambient's default) | the CIDR prefixes the mesh allocates egress addresses from, inside `240.0.0.0/4` or `2001:2::/48`. Any other range, or a value without `mesh-protected=true`, refuses the boot |

The hosts admitted at a mesh egress address are not authored here: they are
[`egress`](#egress)/`HOSTS`, which Codefly generates from the environment's
egress declaration. A declared host is admitted only at an address inside the
range, only when it is the host the client asked for, and only when
`mesh-protected=true`; without the mesh assertion declared hosts are inert. An
IP literal, an undeclared host resolving into the range, and every private,
loopback, link-local or metadata address stay refused. See
[Mesh egress](../CALLABLE_SOURCES.md#mesh-egress).

## egress

Codefly-owned: the module ships no file for it, and render refuses an authored
one. Codefly generates it from the environment's `egress` declaration and
delivers it to accounts, which lists `egress` among its workspace
configuration dependencies.

| Key | Empty means | Holds |
| --- | --- | --- |
| `HOSTS` | the environment declares no egress: no host is admitted at a mesh egress address, and every guarded egress path admits public addresses only | the hosts the environment declares as external reach, `host:port` (443 when the declaration omits the port), comma-separated and sorted. Admitted at a mesh egress address only with `internal-transport/mesh-protected=true`; inert otherwise |

## datasource-oauth

Accounts reads three JSON maps and one local-only list. This version supports
one deployment app, keyed by `api`; other provider keys are refused, so
configuration cannot replace a connector-owned linker:

| Key | Delivery | Content |
| --- | --- | --- |
| `providers` | value | `authorize_url`, `token_url`, and `scopes` for each provider |
| `client_id` | value | The deployment's registered client identifier |
| `client_secret` | secret | The confidential client secret; omit for public clients |
| `local_egress` | value | Local runtime only: loopback `ip:port` entries a callable source may dial. Empty by default |

`local_egress` lets a host booted on a developer's machine reach a stub OAuth
provider and API beside it, for example `127.0.0.1:18080,[::1]:18081`. Each
entry is one loopback IP literal and one port, written canonically: no hostname,
range, CIDR, wildcard, zone or port 0. It applies to every callable-source dial,
both the token exchange and the operation call. Any non-empty value refuses
startup unless the Codefly environment is `local` **and** the process is not in
a Kubernetes pod. A deployed composition that sets it does not start. See
[Local egress](../CALLABLE_SOURCES.md#local-egress).

The local provider and client-id maps are empty, which disables generic sign-in.
The secret map contains only a local placeholder for `api`. For example,
`providers={"api":{"authorize_url":"https://auth.example.com/authorize","token_url":"https://auth.example.com/token","scopes":["read","offline_access"]}}`
and `client_id={"api":"example-client"}` configure the public metadata. The
operator supplies the matching secret map through Codefly's secret provider.
Both endpoints must use HTTPS. Register the host callback URI with the provider.

The app registration is deployment-wide. Per-organization provider app
registrations are outside this version. The source's refresh token, cached
access token and expiry use the existing `datasourceCipher` envelope. The app
client ID, secret, token URL and scopes are resolved from this group on refresh,
so rotating the deployment registration does not leave stale per-source copies; this group adds no credential store. The PKCE verifier stays server-side
and state is bound to organization, person, connector, source, and callback.
