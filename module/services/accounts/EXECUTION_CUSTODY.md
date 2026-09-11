# Private bounded execution custody

This candidate adds an opt-in Accounts-owned broker around the canonical Work
Context issuer. It retains an authenticated owner's exact original delegation
and returns bounded children to the configured consumer workload after the
originating HTTP request is gone. The ordinary Accounts host mounts it only when
its private deployment projection is configured. Local proof does not establish
managed hosting acceptance.

## Served and client contract

`code/pkg/adapters.NewExecutionCustodyServer` constructs the private HTTPS
server. The owner composition supplies the configured WorkContextAuthorityServer,
its real JWTMinter, existing PostgresStore and VaultClient, an immutable consumer
policy map, a server certificate and a client CA pool. Serve it with `ServeTLS`;
TLS 1.3, bounded body/header/time limits, strict JSON and no-store responses are
installed. Never mount it on a public or browser-facing route or wrap it with
request/response body logging. It is deliberately absent from public gateway
catalogs. Missing dependencies fail startup. There is no environment-only enable
switch or unverified fallback.

The versioned wire types and non-retrying HTTPS client are in
`code/pkg/executioncustody`. This is source delivered with Accounts' existing
`accounts` Go module, not a newly published standalone SDK. The signed credentials
remain canonical Codefly WorkContextV1 tokens. Work Context child derivation shares
`exchangeVerifiedParent` with the existing `ExchangeAudience` RPC; the broker has
its own explicit caller-authentication contract before that shared enforcement.

`POST /private/v1/execution-custody/register` takes:

```json
{
  "binding": {
    "org_id": "00000000-0000-4000-8000-000000000001",
    "owner_id": "00000000-0000-4000-8000-000000000002",
    "admission_id": "original-consumer-admission-key",
    "intent_digest": "64-lowercase-hex-characters",
    "task_id": "signed-work-context-task-id",
    "session_id": "signed-work-context-session-id",
    "consumer": "example-worker",
    "profile": "example-profile@immutable-version"
  },
  "parent_token": "PRIVATE ORIGINAL WORK CONTEXT",
  "task_expires_at": 1789139000
}
```

The Authorization bearer is a real Accounts access token. Forwarded owner,
gateway, revision, module-principal and internal-token headers provide no
admission authority. The existing minter verifies access-token signature,
issuer, audience and configured revocation policy; the owner/org membership and
parent/current actor authority checks are canonical Accounts enforcement. The
binding also retains real actor and access-delegation provenance. No owner access
token is persisted. The original owner may recover with a refreshed access token
only while the original execution delegation remains valid.

`admission_id` is 1–128 ASCII alphanumeric or `._:-` characters, scoped by verified
organization and owner. It must be durable before the request. `task_id` is the
signed WorkContext TaskId, not a post-admission execution engine row ID.
`task_expires_at` is an immutable absolute requested upper bound, not a renewed
TTL. `task_claims_digest` must be absent/empty on registration input.

The broker derives the Task child with the configured exact Task scope/actions
and the signed TaskId as resource ID. It inserts the encrypted original parent,
original Task child and immutable binding together before returning:

```json
{
  "reference": "opaque-uuid",
  "binding": { "task_claims_digest": "wc-claims-proto-v1:..." },
  "expires_at": 1789138997,
  "task_token": "PRIVATE ORIGINAL TASK CHILD"
}
```

`binding` includes every input field plus the claims digest. An insert race may
produce discarded signing candidates, but only one immutable ciphertext/reference
wins and every successful caller receives its original Task child. No Task or
other downstream effect is dispatched by registration. A same-owner/admission
retry with different parent bytes, request identity, intent, policy or absolute
horizon conflicts, including a different token with the same actor lineage. A
lost acknowledgement can be recovered by repeating the original registration input;
ordinary successful replays do not issue a new child. Parent and Task bearers must
stay private in server transport/memory and encrypted custody.

`POST /private/v1/execution-custody/recover` is the read-only owner-authenticated
path when the originating request and original parent bytes have disappeared.
It accepts `{binding, task_expires_at}` containing the exact original non-secret
registration inputs (no claims digest yet). It reads the existing original
admission, checks the immutable intent, real actor/delegation and current retained
parent authority, and returns the same Registration. It never signs, creates or
substitutes a parent; NotFound remains unresolved. A changed owner or intent is
denied. Persist those non-secret original inputs before calling Register so they
survive a lost reply and process replacement. After receiving any registration
result, persist its non-secret reference and full returned binding before dispatch.

`POST /private/v1/execution-custody/exchange` takes the opaque `reference`, the
**entire returned binding**, exact configured `audience`, and boolean `lookup`.
A verified client certificate must have exactly one URI SAN equal to that
consumer policy's `spiffe://...` worker identity. A replacement instance may have
a new certificate for that same workload identity. Certificate roots and issuance
are deployment-owned inputs. Caller headers never establish this identity.
Workers get no Accounts owner token, gateway trust token, database login or Vault
credential. The opaque reference alone is insufficient.

The broker compares all binding fields, the immutable policy fingerprint and the
signed Task claims digest, then re-verifies the original parent and all current
Accounts authority/revocation facts. The only database scope it establishes comes
from the authenticated encrypted binding. It does not fabricate an owner session
or invoke the tenant exchange using impersonation headers. It returns `{token,
expires_at}` with configured model/resource actions. `lookup=true` gets only the
configured read action; execute gets exactly invoke and read. No request scope or
TTL override is accepted. Children are capped at the original parent's expiry
and the retained original Task child's signed expiry, reserving three seconds.

The consumer must independently verify child signature/current revision, exact
profile/audience/actions/resources, immutable actor/owner/Task/session lineage,
replay policy and its sealed Task horizon. Its immutable operation keys and
intent digest must match its original durable admission. This broker never
creates, changes or retries execution operation keys. Resolving a missing or
inconclusive admission/effect receipt must remain unresolved; custody success is
not permission to issue another Start or resample an effect.

## Claims digest v1

`wc-claims-proto-v1:` plus lowercase SHA-256 hex of deterministic protobuf
serialization of decoded canonical Codefly WorkContextV1. Include issuance,
nonce, key, lineage, scopes, revision and expiry; do not hash bearer bytes or JSON
formatting. Unknown fields are rejected. `executioncustody.ClaimsDigest` is the
canonical helper and `digest_test.go` supplies a fixed wire-byte test vector.
Consumers storing protobuf JSON evidence decode it first. A different digest
version is a different contract and cannot be silently adopted.

## Storage, lifetime and rotation

Migration `131_execution_custody` is applied through the existing store service's
migration identity, before enabling this composition. The relation is tenant-owned,
forced RLS and deny-all for tenant traffic. Only the existing Accounts control
plane can select/insert/delete, with UPDATE confined to ciphertext erasure. Normal
request and worker roles receive no custody grants. Its three narrow PostgresStore
methods use the existing audited control-plane transaction path with explicit
reference or organization/owner/admission filters. All live authority reads still
use the verified service-postgres reader boundary. No consumer gets migration or
control-plane capability.

Use normal `infra.NewPostgresStore`, or the same production boundary's
`NewPostgresStoreWithCapabilities` with primitive-projected distinct non-owner
reader/writer secrets. Existing token-file rotation hooks remain installed. The
owning primitive's physical identity separation/managed transport gates remain
applicable; this candidate does not close them.

VaultClient uses the existing `api-keys` transit key and versioned SecretCipher
envelope. Its encrypted purpose is `execution-custody/<opaque-reference>`.
Production obtains Vault address/token through the existing Codefly vault
configuration/secret projection. An explicitly scoped Vault policy needs only
`update` on `transit/encrypt/api-keys` and `transit/decrypt/api-keys`. A worker has
neither permission. The local qualification creates this real scoped token using
a disposable initialized/unsealed persistent Vault; its administrator credential
never enters the broker configuration. Managed Vault identity, TLS, projection,
backup/restore and availability are separate deployment gates.

The ordinary Accounts host calls `PurgeExecutionCustody(ctx, time.Now())` at
startup and on its existing one-minute retention/reconcile tick, including when
the listener is disabled. It erases expired ciphertext and
retains non-authorizing immutable registration tombstones. Expiry denies exchange
even if cleanup is delayed. Tombstones prevent later registration from renewing
an old admission identity; they are retained until owner/org deletion, which
cascades the records. Backup retention can retain old ciphertext, so primitive
backup/deletion policy still applies. Restoring old data never overrides signature,
expiry, current revision or actor revocation checks. No cache survives as an
alternate custody authority.

Vault key rotation preserves existing envelopes. Work Context signing-key
replacement follows the canonical SDK's own-key exchange behavior: old-parent
exchange fails closed if its original signing key is no longer active. The broker
never substitutes a new parent to recover availability. Keep the original signer
available for its bounded horizon, or accept denial; a more general signing-key
overlap protocol requires separate canonical SDK qualification. TLS workload
certificate replacement may keep the same authenticated URI identity; changing
consumer identity/profile/policy fences old bindings.

## Errors and operator recovery

All responses are credential-free errors `{code}` with no upstream detail.
Invalid JSON/unknown fields are 400 InvalidArgument; absent/invalid access or
worker identity is 401 Unauthenticated; substitution/invalid original authority is
403 PermissionDenied; missing custody is 404 NotFound; conflicting registration
is 409 AlreadyExists; stale revision/expired retained horizon is 412
FailedPrecondition; unavailable custody, Vault or Accounts is 503 Unavailable.
No automatic redirect or retry is allowed. A transport failure or 503 after a
write may mean it committed. Recover only using the same original registration
identity/input, through Recover when original parent bytes are unavailable. Inspect original non-secret receipt keys when live authority is
no longer valid; never issue new work to resolve uncertainty.

## Local qualification and remaining gates

From the repository root, with local Docker, psql and Go installed:

```sh
python3 qualification/execution-custody/run.py
cd module/services/accounts/code
go test -race ./pkg/adapters ./pkg/executioncustody
go vet ./pkg/adapters ./pkg/executioncustody ./pkg/infra
```

The runner creates and removes its own loopback-only PostgreSQL and persistent
Vault containers, applies every owning store migration and uses real JWT sessions,
Vault encryption/scoped access, database roles and client-certificate verification.
It never changes a managed resource, production credential, IAM grant or paid
provider. Local certificates, database bootstrap and unseal are fixture inputs;
this is not external IdP sign-in or managed-hosting qualification.

A consumer's separate joined original-key receipt recovery test must use this
candidate with its actual execution/worker/downstream processes and independent
effect counts. Native broker tests are not that joined proof. Record exact source
pins and evidence for both candidates. Public image/package publication, managed
private route/workload certificate issuance, database/Vault identity and transport,
observed retention execution, managed restart/restore and signed-in hosted composition
remain reviewed production gates. No hosting readiness follows from this PR.


## Actual local process for a joined consumer proof

Build the owning executable from `code/`:

```sh
go build -trimpath -o /absolute/private/output/accounts-custody ./cmd/custody-qualification
/absolute/private/output/accounts-custody --local-qualification \
  --config /absolute/private/fixture/config.json --max-runtime 15m
```

This explicitly local executable is not a production bootstrap. It refuses
non-loopback PostgreSQL/Vault URLs, always binds fresh loopback ports and exits
within at most 30 minutes. It performs no migration, seed, grant or Vault admin
operation. The fixture owner must provision the owning migrations, existing
organization/member/principal/RBAC facts and scoped Vault transit credential
before starting it. The qualification runner demonstrates that setup against
actual local services; it does not synthesize owner headers or a custody server.

The JSON config (a regular 0600 file) supplies:

- `reader_url`, `writer_url`: distinct primitive runtime database identities;
  only loopback PostgreSQL URLs with `sslmode`/`pool_max_conns` query options.
- `vault_url`, `vault_token`: local initialized/unsealed Vault and the real
  scoped transit-only token. Never the bootstrap administrator token.
- `signing_key_file`: 0600 PKCS8 PEM Ed25519 private key, retained across process
  replacements. Work Context and owner access signatures use the same public
  key with the minter's canonical derived key ID, but distinct issuer/audience
  verification settings.
- `tls_cert_file`, `tls_key_file`, `client_ca_file`: verified server TLS
  certificate/key and worker client CA. Private keys must be regular 0600 files.
  Worker certificate URI SAN must match the selected consumer policy.
- `internal_credential_file`: a separate private revision credential of at least
  32 characters. It has no tenant mint, custody registration or worker exchange
  authority.
- `owner_id`, `org_id`: existing fixture membership for the real JWTMinter's
  admission session; `issuer`, `auth_issuer`, `auth_audience`: exact trust values.
- `consumers`: map of configured ExecutionConsumerPolicy values. JSON policy
  keys are `WorkerURI`, `ParentAudience`, `TaskAudience`, `Audience`, `Profile`,
  `ResourceKind`, `ResourceID`, `InvokeAction`, `ReadAction`, `TaskResourceKind`
  and `TaskActions`. Task actions must match the independently qualified
  consumer's supported canonical action set.
- `owner_token_file`, `state_file`: absolute output paths in a 0700 directory.
  Both are atomically written 0600. The owner token is a real Accounts access
  JWT for **admission only**, never projected into a replacement worker.

State is non-secret JSON with `broker_url`, `tenant_grpc`, `internal_grpc` and
`jwks_url`. Listeners use verified TLS. The tenant gRPC listener serves canonical
WorkContextService under the actual existing owner JWT policy; obtain the
original parent through its authenticated StartTask RPC. The separate internal
gRPC listener serves the canonical current-revision/evidence policies and rejects
tenant issuance. `/v1/auth/.well-known/jwks.json` on the broker's TLS listener
returns the canonical public key set. No parent token is written by the process.

On restart, reuse the same database/Vault/signing inputs, then read the fresh
listener state and admission token. Original registration recovery uses only its
persisted non-secret input through Recover. Workers use the same retained opaque
binding and their mTLS identity to request children. The local acceptance launches
this executable as an actual OS subprocess, obtains its parent via authenticated
gRPC, registers custody, discards the request/parent, terminates the process,
launches a replacement and recovers the byte-identical original Task child before
requesting a bounded lookup child. This supplements the earlier component proof;
consumer Task/worker/model process counts remain the consumer's joined proof.


## Normal Accounts host and projected configuration

`code/execution_custody.go` is called from the ordinary `work.go` bootstrap; no
fixture executable is used for deployment. The constructor shares the existing
Accounts JWT minter, stable Vault signing key (`saas-starter` issuer/current key
ID), PostgreSQL session/authority store and Vault cipher. A private stable
instance of the canonical WorkContextAuthorityServer avoids concurrent mutation
by generated server setup. Enabling requires the real Redis revoker to have been
wired and `ACCOUNTS_REVOCATION_FAIL_OPEN` to be false.

The existing `security` workspace configuration supplies
`EXECUTION_CUSTODY_CONFIG_FILE`, an absolute private JSON projection. Empty leaves
all private listeners disabled. The JSON shape is:

```json
{
  "enable_tenant": false,
  "tls_cert_file": "/var/run/accounts/custody/tls.crt",
  "tls_key_file": "/var/run/accounts/custody/tls.key",
  "client_ca_file": "/var/run/accounts/custody/client-ca.crt",
  "consumers": {
    "example": {
      "WorkerURI": "spiffe://example.invalid/ns/example/sa/example-worker",
      "ParentAudience": "example-admission",
      "TaskAudience": "example-task",
      "Audience": "example-model",
      "Profile": "example-profile@immutable-version",
      "ResourceKind": "example-model",
      "ResourceID": "example-resource",
      "InvokeAction": "invoke",
      "ReadAction": "read",
      "TaskResourceKind": "example-task",
      "TaskActions": ["execute", "read"]
    }
  }
}
```

These are placeholders; the composing consumer owns the exact policy. JSON and
all referenced files must be regular private files (0400/0440), at most128KiB.
Atomic Kubernetes projected-secret symlinks are supported. TLS trust, certificate
and consumer policy are snapshotted at startup; roll Accounts after changing
them. Policy changes fence old bindings; certificate replacement retaining the
same URI can preserve worker access within the original horizon.

The authoritative topology declares three module-facing TCP endpoints, resolved
by exact endpoint name through the Codefly SDK. No fixed runtime port is embedded
in the process and no default consumer network edge is granted:

| Endpoint | Topology port | Authentication and surface |
|---|---:|---|
| `accounts/custody` |9443| TLS1.3 server identity; real owner JWT for Register/Recover, verified worker URI client certificate for Exchange |
| `accounts/revision` |9444| TLS1.3 server identity plus the existing separate `x-codefly-internal-token`; canonical internal WorkContext RPC policy, no caller certificate required; tenant issuance/exchange denied |
| `accounts/tenant` |9445| Opt-in `enable_tenant: true`; native gRPC over TLS1.3 (ALPN h2), real owner access JWT and canonical tenant WorkContext policy; internal revision/evidence RPCs denied |

The revision listener uses the already configured `CODEFLY_INTERNAL_TOKEN`
(minimum32 characters), including the existing previous-token overlap policy.
It does not accept that token as owner or worker custody authority. All enabled listeners
use the projected server certificate; its DNS SANs must cover their private
route(s). Compositions declare only required endpoint dependencies and render
network policy. Preserve TLS through a TCP passthrough; never synthesize client
identity from mesh/HTTP headers. This listener does not change the generated
h2c internal transport. Shutdown drains the private listeners before stores close.

For Vault, existing Codefly `vault/address` and `vault/token` projections remain
compatible. Optional `vault/ca-file=/var/run/accounts/vault/ca.crt` and
`vault/token-file=/var/run/accounts/vault/token` configuration select private
mounted CA/token files (same0400/0440 atomic-file rules). Selecting either requires
HTTPS. The token is reread for every Transit request; missing or malformed files
fail closed without static-token fallback. Redirects and ambient HTTP proxies
are disabled. The signing-key KV loader uses the same transport and current token
at startup. CA changes require restart. The normal service additionally needs its
existing KV signing-key and Transit hash/HMAC permissions; the custody-only test
policy is not the full normal Accounts ACL.

## Build, release and deployment gates

From `code/`, `go build -trimpath -o /absolute/output/accounts .` builds the normal
Accounts executable. The module's existing pinned `go-grpc`0.1.36 service agent
owns its container build; `.github/workflows/ci.yml` runs the pinned Codefly0.1.145
`codefly ci run --head <source> --phase build --output <evidence> --jobs2` contract
(with the selection arguments shown in that workflow). Run the canonical full
`codefly ci run` gates for release. Do not replace these with the local custody
fixture or a hand-authored deployment image.

The existing aggregate `release-gates` must pass before publication. The normal
module/package release owner and consuming deployment owner then record the
immutable image reference/digest, render actual projections and explicitly approve
the managed deployment. A locally built executable or CI image build is not an
image publication. Managed database migration/identity, durable initialized Vault,
private DNS/network/TLS, workload credential projection and actual signed-in
consumer join remain required. No cloud, IAM or production credential action is
performed by this source change.


## Explicit database transport for hosted custody

The existing `security` workspace group supplies `ACCOUNTS_DATABASE_TRANSPORT`
(environment fallback uses that same name). Values are `verified-tls` and
`local-identity-proxy`. An empty value preserves existing normal Accounts consumer
behavior: the Codefly connection URL is parsed by pgx, including its legacy
settings. **Empty does not assert verified TLS and cannot enable the private
hosted custody/revision mount.** Existing local qualification uses its separately
bounded fixture bootstrap. Unknown profiles fail startup.

`verified-tls` requires explicit PostgreSQL user, database and TCP hostname,
`sslmode=verify-full`, actual driver certificate/hostname verification on every
fallback, and no ambiguous endpoint/user/database/role query override or ambient
`PG*` setting. Only lowercase `sslmode`, `sslrootcert`, `sslcert`, `sslkey`,
`connect_timeout`, the documented pgx pool size/lifetime/health settings and
`application_name` are accepted. Parsed PostgreSQL runtime parameters may contain
only that application label. Role/session-authorization/options aliases in any
case and NUL-containing values are rejected. The normal direct credential hook
remains in force.

`local-identity-proxy` requires each existing Codefly store/postgres read-only and
read-write secret to contain a hostless URL of this form:

```text
postgresql://principal@/database?host=%2Fprivate%2Fsocket&port=5432&sslmode=disable&passfile=%2Fdev%2Fnull
```

The Unix directory must be absolute, normalized and short enough for the local
socket. User/database/port must be explicit; passwords (including empty userinfo
passwords), unknown/duplicate parameters, URL roles, service/passfile overrides,
TCP/driver fallback, ambient `PG*` environment and `POSTGRES_TOKEN_FILE` are denied.
The parsed driver's socket/user/database/port must exactly match the projection,
with no TLS or password on that local hop. Reader and writer must use distinct
private sockets and distinct non-owner principals. There is no application cloud
SDK or token minting. The platform owns each proxy's fixed principal, verified
remote TLS/IAM, private routes, mounted socket ownership and reconnect lifecycle.

Every normal Accounts pool uses the selected parser: scoped reader/writer,
legacy request pool and billing/webhook/job worker pools. The existing factory
and fixed `SET ROLE` boundaries still own authority; a connection URL cannot select
a privileged role. Changing transport is an explicit deployment change, not a
fallback after a failed TLS or identity connection.

Local Accounts proof (requires local `initdb`/`pg_ctl`):

```sh
cd module/services/accounts/code
go test -race pkg/infra/database_transport.go pkg/infra/database_transport_test.go
go test -race -tags=integration ./pkg/adapters -run TestAccountsLocalProxyStore -count=1 -v
```

The second test creates a disposable Unix-only PostgreSQL server, applies all127
unchanged migrations, opens Accounts' actual factory with two socket/principal
bindings, checks direct/tenant custody denial, recovers the original private
record after store reconstruction, and opens all three fixed worker pools with
custody denied. It is an Accounts local-socket/role proof, not a Cloud SQL IAM,
managed proxy, remote TLS or full hosted-process acceptance result. The exact
normal image must include this follow-up before consuming the proxy profile.

## Opt-in normal tenant WorkContext TLS

The `enable_tenant` projection defaults to false, preserving existing deployments.
When true, the normal host mounts the existing canonical owner-authenticated
WorkContext gRPC constructor separately from revision. The original minter,
Vault signing key, PostgreSQL authority and Redis revoker remain shared. The
caller uses `accounts/tenant` discovery as a `DNS:port` native gRPC target with
verified TLS1.3 and the server CA; each tenant call supplies the original Accounts
access JWT in `authorization: Bearer ...`. No client certificate or internal
credential substitutes for the owner JWT. StartTask and ExchangeAudience retain
all existing tenant, owner, current-authority, actor and bounded-parent checks.
The listener exposes the canonical tenant WorkContext methods; it is not a new
renewal protocol. Internal revision/evidence methods remain denied.

Bind all enabled private endpoints before serving any of them. A failed endpoint
bind closes earlier listeners and fails startup. Shutdown concurrently drains
both gRPC listeners and the custody HTTP server with a shared five-second bound;
forced stop remains the timeout fallback. Normal login, registration, JWKS,
REST/Connect and legacy internal listeners are unchanged. A composing consumer
must explicitly declare narrow reachability to tenant; no public route or default
consumer edge is added. The projected server SAN must match its private DNS.
Do not point a tenant client at revision or infer native gRPC support from a
browser HTTPS route.

Focused local proof: `python3 qualification/execution-custody/run.py --tenant-mount`
from the repository root. It provisions disposable loopback PostgreSQL/Vault and
Redis, then exercises the actual normal-host projection, construction and
start/shutdown functions with real JWT sessions and native grpc-go clients.
Only endpoint discovery is replaced by allocated loopback sockets. It covers
explicit opt-in, bind rollback, real StartTask/ExchangeAudience, missing/forged
and revision-only credentials, TLS CA/hostname/version/plaintext denial,
internal/tenant exposure separation, shutdown/rebind with the same original
parent, and actual Redis session revocation. This proves the mount, not complete
managed bootstrap, a proxy/database transport, or an external IdP ceremony.
