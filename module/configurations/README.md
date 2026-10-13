# Host configuration groups

Each environment declares values in `<group>.env` and secrets in
`<group>.secret.env`. Codefly delivers these to services declaring the group;
consuming workspaces override a group rather than editing module-owned files.

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
