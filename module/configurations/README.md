# Host configuration groups

Each environment declares values in `<group>.env` and secrets in
`<group>.secret.env`. Codefly delivers these to services declaring the group;
consuming workspaces override a group rather than editing module-owned files.

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
it reads is declared in that group's default here. Its
`keysNoGroupCanDeclare` list names the few that stay inert and why; the list
shrinks and never grows.

## security

Session, MFA and WebAuthn policy, plus three operator switches whose empty value
is the shipped behaviour:

| Key | Empty means | Set to |
| --- | --- | --- |
| `TRUSTED_PROXY_CIDRS` | no forwarding header is trusted, so every per-IP budget buckets on the peer — behind an ingress and a frontend hop, one bucket for the whole cell | the cell's ingress and frontend ranges, comma-separated exact IPs or CIDRs. A malformed entry fails the boot rather than trusting a spoofable header. Distinct from the identically named key in the `gateway` group, which is auth-gateway's own edge and which a deployed gateway refuses to start without |
| `ACCOUNTS_REVOCATION_FAIL_OPEN` | a token whose revocation status cannot be read is denied — fail **closed** | `true`, only when fronting accounts directly with no auth-gateway, to keep the direct verify path serving through a revocation-store outage at the cost of a revoked token's remaining TTL |
| `RBAC_CENTRAL_ENFORCEMENT` | `shadow`: request-time central RBAC admission classifies only | `enforce` to hard-deny. Any other value fails the boot, so a typo cannot leave enforcement off |

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
