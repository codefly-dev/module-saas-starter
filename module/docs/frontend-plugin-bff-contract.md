# Frontend plugin BFF contract

Status: active (`FP-008`, `FP-030`, `FP-031`, `FP-032`, `FP-033`, `FP-034`, `FP-043`)
Route: `/api/plugins/{plugin}/{alias}/{relative-path}`

This is the generic same-origin transport for trusted compile-time frontend
plugins. Consuming solutions, Codefly-owned products, and future consumers use the same
route and policy. It is not a general reverse proxy and does not accept a URL,
host, port, module, service, endpoint, or upstream prefix from a browser.

## Resolution

1. `{plugin, alias}` must match exactly one entry in the generated installed
   service allowlist.
2. That entry supplies the validated route prefix and logical Codefly
   `{module, service, endpoint}` target.
3. The server resolves exactly one matching endpoint from Codefly's private
   environment. Missing, duplicate, malformed, credential-bearing, non-HTTP(S),
   path-bearing, query-bearing, or fragment-bearing addresses are unavailable.
4. The relative path is appended to the installed prefix one safe segment at a
   time. Dot segments, slashes, backslashes, control values, encoded separators,
   encoded percent signs, oversized segments, and empty segments fail closed.
   The **last** segment may also carry a Google custom verb — `<resource>:<verb>`
   with one colon and a lowerCamelCase verb — and its colon is forwarded
   literal. A colon anywhere else in the path, a second colon in one segment, an
   empty resource or verb, and a non-lowerCamelCase verb all fail closed.

## Which bindings the transport carries

A contributed page reaches exactly what the module's own bindings declare, in
both shapes those bindings come in.

**REST, including custom verbs.** A method that is not one of a resource's
standard verbs is bound in the Google HTTP form as `…/<collection>:<verb>` —
`records:recover`, `records/{record_id}/commands:recover`,
`records/{record_id}/interactions/{interaction_id}:respond`,
`triggers/{name}:fire`. The colon is what separates the verb from the resource,
so percent-encoding it names a different resource and the binding is never hit.
Before this rule, both halves of the transport — the browser client and this BFF
— matched every segment against `[A-Za-z0-9._~-]+`, so those four routes were
refused in the browser before a request left it, and a contributed page reached
LESS than the module served: a binding written in that form was not available
through the transport at all, and a composition had to reach it some other way.

**Connect procedures.** They are carried, and a requirement says so by
declaring `/` as its `routePrefix`. The Connect protocol puts a unary procedure
at `/<fully.qualified.Service>/<Method>` from the origin, which is where this
contract's own fixed capability handshake has always been probed — so a Connect
backend serving where the protocol says had no prefix it could declare, and a
composition had to take a second Connect transport from somewhere else. The root
prefix is Connect-only: a REST requirement declaring it would turn its alias
into a prefix-less proxy of the whole upstream. On a Connect alias the relative
path must name exactly one procedure (a dotted proto full name, then a method
name); any other shape is `400 invalid_path`, which is a narrowing — the
protocol serves nothing else at that path — and never reaches the upstream.

A contributed page is therefore *not* restricted to REST bindings. What it may
not do is choose a prefix: both shapes are reachable only under the alias its
own requirement declared, and a route outside that alias is still
`404 plugin_service_not_found`.

The **playground** for all of this is `/admin/platform/plugin-transport`
(super-admin): it sends every shape through the real transport and shows which
of the three answers came back — refused in the browser, a host problem, or the
upstream's own forwarded answer. Its route list is
`src/features/plugin-transport/model/demo-plugin.ts`, a demo plugin the
canonical host declares and does not install, so every alias answers
`plugin_service_not_found` here — which is already proof the shape was carried,
because that refusal comes from resolution rather than from the path gate. The
forwarded answer is proved by
`src/features/plugin-transport/ui/plugin-transport-playground.container.test.tsx`,
which drives the published client transport into this handler over a stub
upstream.

Concrete endpoint addresses remain in the server runtime. They never enter
`frontend.config.ts`, plugin metadata, public environment variables, client
source, error bodies, or response headers.

## Reserved capability probe

`GET /api/plugins/{plugin}/{alias}/.well-known/capabilities` is the only
host-reserved relative path. It is not forwarded to the product route prefix.
After ordinary authentication, origin, allowlist, and Codefly endpoint
resolution, the BFF calls exactly one protocol-defined backend operation:

| Installed protocol | Fixed backend operation |
| --- | --- |
| REST | `GET /.well-known/codefly/frontend-plugin-capabilities` |
| Connect | `POST /saas.frontend.plugin.v1.FrontendPluginCapabilityService/GetFrontendPluginCapabilities` with the generated empty ProtoJSON request |

The source of truth is
`@codefly-dev/saas-plugin-contract/proto/saas/frontend/plugin/v1/capabilities.proto`.
The response is bounded to 16 KiB, must be JSON, must use schema version `1`,
and must exactly match the installed contract ID and major. The BFF returns a
normalized response containing only those fields and sorted capability IDs; it
never proxies the raw handshake body. Missing operations, invalid ProtoJSON,
unknown fields, unsafe or duplicate IDs, and contract mismatches return
`426 backend_incompatible`.

The fixed capability operation is metadata-only and must not require a product
permission. The BFF itself still requires the authenticated host bearer and
forwards it; backend implementations may validate it but cannot return tenant
data from this operation. A backend `401` from this operation remains the safe
`401 authentication_required` host problem; an expired credential is not a
backend compatibility failure.

## Request policy

| Concern | Policy |
| --- | --- |
| Authentication | Require one syntactically valid `Authorization: Bearer …` credential and forward it unchanged. The `codefly_session` presence cookie is never trusted. |
| Identity and tenant | Never accept or forward caller `x-user-*`, `x-org-*`, `x-tenant-*`, role, session, gateway, internal-token, cookie, host, or forwarding headers. The downstream gateway/service validates the bearer and derives authoritative identity and tenant scope. |
| Origin | If `Origin` is present it must equal the BFF origin. Browser `Sec-Fetch-Site` must be `same-origin` or `none`. No CORS permission is emitted. |
| Methods | Product traffic: REST permits `GET`, `POST`, `PUT`, `PATCH`, `DELETE`; Connect permits `POST` on a procedure path (the path is checked first, so a non-procedure path on a Connect alias is `400`, not `405`). The reserved browser capability probe permits only `GET` and translates to the protocol-specific backend operation. `HEAD`, `OPTIONS`, trace/tunnel methods, and protocol mismatches return `405`. |
| Request headers | Only bearer, accept, language, content type, conditional ETags, required Connect protocol/timeout headers, and a structurally valid non-zero W3C `traceparent` are forwarded, through the frontend's one shared parser (`src/lib/trace-context.ts`), which requires the specification's lowercase hex and does not lowercase a value into validity. The host generates `x-request-id`; malformed trace context is dropped, and `tracestate` and `baggage` are not forwarded by this boundary. |
| Request media | JSON and `+json`; Connect additionally permits protobuf media. Content encoding and form/multipart uploads are not supported in v1. |
| Limits | Request body: 1 MiB. Response body: 5 MiB. Whole upstream operation: 10 seconds. Calls are unary and uncached. |
| Redirects | Fetch uses manual redirects. Upstream redirects become a contained `502`; `Location` is never exposed or followed. |
| Responses | Only content type/language, ETag, last-modified, retry-after, and vary are retained. Cookies, CORS, location, server, forwarding, hop-by-hop, and private headers are removed. |
| Cancellation | Client aborts cancel the upstream request. Timeouts and network failures map to separate stable problems. |

## Request correlation

The BFF generates one fresh `x-request-id` for every attempt before performing
authentication or resolution. Caller and backend request/correlation IDs are
ignored; the generated value is forwarded to the backend and returned on every
browser response. Host problem bodies repeat the same value as `requestId`.
Retries receive new IDs.

The public runtime trusts only the bounded BFF response header, never a
`requestId` supplied in a product problem body. W3C `traceparent` is separately
validated, remains untrusted propagation input, and is never the public support
identifier. The complete rules are frozen in the
[request-correlation contract](frontend-plugin-request-correlation.md).

The BFF is a network and transport boundary, not the backend authorization
authority. Every product endpoint behind it must validate the forwarded bearer,
derive tenant context from validated claims, enforce semantic authorization,
and reject cross-tenant resource identifiers. The end-to-end expired,
wrong-organization, support, super-admin, and cross-tenant requirements are
defined by the
[authentication and tenant matrix](frontend-plugin-auth-tenant-matrix.md).
Frontend-only tests cannot replace its product-backend rows.

## Stable problems

Failures use `application/problem+json`, `Cache-Control: no-store`, a generated
`x-request-id`, and a non-sensitive `urn:codefly:problem:frontend-plugin-bff:*`
type. Current status mapping:

| Status | Codes |
| --- | --- |
| `400` | `invalid_path`, `invalid_body` |
| `401` | `authentication_required` |
| `403` | `cross_origin_request` |
| `404` | `plugin_service_not_found` |
| `405` | `method_not_allowed` |
| `413` | `request_too_large` |
| `415` | `unsupported_media_type` |
| `426` | `backend_incompatible` |
| `499` | `client_closed_request` |
| `502` | `upstream_failed`, `upstream_redirect`, `upstream_response_too_large` |
| `503` | `backend_unavailable` |
| `504` | `upstream_timeout` |

Unknown services and unavailable services are deliberately distinct so a
product controller can render “not installed” separately from “installed but
unavailable” without learning any endpoint location.
