# Private bounded execution custody

This candidate adds an opt-in Accounts-owned broker around the canonical Work
Context issuer. It retains an authenticated owner's exact original delegation
and returns bounded children to the configured consumer workload after the
originating HTTP request is gone. It is a composition library, not an enabled
public route, another issuer, an execution engine, or a deployable hosting claim.

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

Schedule `PurgeExecutionCustody(ctx, time.Now())` using the existing owner's
retention execution, at least once per minute. It erases expired ciphertext and
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
retention scheduling, managed restart/restore and signed-in hosted composition
remain reviewed production gates. No hosting readiness follows from this PR.
