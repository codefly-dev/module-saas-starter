# Access-token signing-key rotation

The Ed25519 keypair accounts holds is the trust anchor for every credential the
edge verifies locally: access tokens, Work Context capabilities, and
composed-module registration tokens. This is the runbook for replacing it
without rejecting credentials that are still valid.

## What each side does

**accounts** signs with one private key, loaded from Vault, and publishes the
matching public key plus every retained verification key at
`GET /v1/auth/.well-known/jwks.json`. Each key carries a deterministic `kid` —
the first eight bytes of SHA-256 over the public key — so two processes holding
the same key agree on its id with no coordination
(`services/accounts/code/pkg/auth/ed25519/minter.go`). Retained keys come from
the `internal-auth` configuration group:

| Key | Meaning |
| --- | --- |
| `JWT_PREVIOUS_PUBLIC_KEYS` | Comma-separated base64 Ed25519 **public** keys accounts keeps verifying and keeps publishing. Standard or raw-url base64; malformed entries are logged and ignored. |

**auth-gateway** reads that document and verifies by `kid`
(`services/auth-gateway/code/access_keys.go`). It holds the whole published set,
not one key, so both halves of an overlap verify — and it re-reads on its own,
so no gateway restart is part of a rotation.

Timing the runbook depends on:

| Bound | Value | Set in |
| --- | --- | --- |
| Access-token lifetime | 3 min (5 min while impersonating) | `Config.AccessTokenTTL` / `ImpersonationTokenTTL` |
| Clock-skew leeway | 60 s | `tokenClockSkewLeeway`, matched to accounts' `Config.ClockSkew` |
| Gateway key-set TTL | 5 min | `accessJWKSCacheTTL` |
| Gateway stale grace | 10 min | `accessJWKSStaleGrace` |
| Unknown-`kid` refetch spacing | 5 s | `jwksProbeInterval` |

A gateway picks up a newly published key within one TTL at worst, and usually on
the first token that names it: an unrecognised `kid` triggers a refetch, rate-
limited to one per 5 s across all key ids so untrusted token input cannot drive a
fetch per request. A key that stops being published stops verifying within one
TTL — or within TTL + grace if accounts is unreachable for the whole window.

## Rotation

Each step is safe to hold indefinitely; only move on once the previous step has
converged.

1. **Generate and stage.** Create the new keypair and store its private half in
   Vault beside the current one. Nothing changes yet.
2. **Publish.** Append the **incoming public key** to `JWT_PREVIOUS_PUBLIC_KEYS`
   and restart accounts. The JWKS now lists both keys; accounts still signs with
   the old one. Wait one gateway key-set TTL (5 min) and confirm every gateway
   serves the new document — `GET /ready` on each gateway must be 200 and
   `auth_gateway.jwks.refresh{outcome="ok"}` must be advancing.
3. **Switch signing.** Point accounts' Vault signing key at the new key, and
   move the **outgoing public key** into `JWT_PREVIOUS_PUBLIC_KEYS` (dropping the
   incoming one, which is now the signing key and is published automatically).
   Restart accounts. Both keys stay published; new tokens are signed by the new
   key, in-flight tokens still verify under the old one.
4. **Overlap.** Hold for at least the longest access-token lifetime + clock
   skew + gateway key-set TTL — with the defaults (impersonation tokens being
   the longest at 5 min), **11 minutes**; use 30 to leave room for a rolling
   restart. Every token signed by the outgoing key has expired by the end of
   it.
5. **Retire.** Remove the outgoing public key from `JWT_PREVIOUS_PUBLIC_KEYS` and
   restart accounts. Gateways converge within one TTL and reject anything still
   signed by the retired key. Revoke the retired private key in Vault.

**Rollback.** Before step 5 the outgoing key is still published, so rolling back
is step 3 in reverse: point signing back at the old key, keep both public keys
listed, restart accounts. After step 5 the retired key is gone from the
published set, so rolling back to it means republishing it (step 2 with the old
key as the incoming one) before signing with it again.

Rotating the gateway and accounts in either order is safe at every step, because
the gateway never pins a key at boot: a gateway started mid-overlap loads the
whole published set and accepts both keys regardless of the order the document
lists them in.

## Availability and security tradeoff

A gateway that cannot reach the JWKS keeps serving its last good key set for
`accessJWKSCacheTTL + accessJWKSStaleGrace` (15 min with the defaults) and then
fails closed with 503. This is deliberate:

- **Availability.** An accounts restart, deploy, or brief outage must not take
  authentication down with it, and must not leave a gateway permanently unable
  to verify JWTs. A gateway that has never loaded a key set is *not ready* — it
  answers 503 on `/ready` and is not routed traffic — and it recovers on its own
  as soon as accounts answers. No operator action, no restart. A gateway that
  *has* loaded keys stays ready even once they age past the grace: it refuses
  JWTs with 503 per request, but the routes that never carried a token (the
  billing and email webhooks, `/assets`, `/.well-known`) keep being served.
  Withdrawing the whole listener because the key set aged would turn a
  degraded-authentication incident into a total one. Alert on
  `auth_gateway.jwks.refresh{outcome="error"}`, not on readiness, to catch it.
- **Security.** The grace is bounded, so it also bounds retirement: a key
  withdrawn at accounts stops verifying everywhere within 15 minutes even if
  accounts never becomes reachable again. Nothing else is relaxed — an
  unrecognised `kid` selects no key at all rather than falling back to another,
  a token whose signature does not match the key its `kid` names is refused, and
  revocation, issuer, audience, expiry, and the alg lock apply identically under
  either key. Widening the verification set never widens authorization.
- **Emergency revocation.** Because of that bound, a compromised key is not
  retired by editing the JWKS alone. Retire it *and* kill the affected sessions
  through the revocation list, which is consulted per request and is not subject
  to the key cache.

The Work Context path deliberately does not take the grace: a presented
capability is an optional attenuation the caller can retry, so it fails closed
the moment its key set expires.

## Observability

Two counters, both with a fixed label set and no credential-derived values:

| Metric | Labels | Use |
| --- | --- | --- |
| `auth_gateway.jwks.refresh` | `key_set` = `access_token` \| `work_context`, `outcome` = `ok` \| `error` | Refresh health. A sustained `error` streak means the gateway is running on cached keys and will fail closed when the grace expires. |
| `auth_gateway.jwt.rejection` | `reason` = `keys_unavailable` \| `unknown_key_id` \| `ambiguous_key_id` \| `invalid_token` \| `revoked` \| `session_revoked` \| `revocation_unavailable` | Why tokens are being refused. `unknown_key_id` rising during a rotation means a gateway has not converged on the published set; `keys_unavailable` means it holds none at all. |

During a rotation, watch `unknown_key_id` fall to zero after step 2 before
starting step 3, and `keys_unavailable` stay at zero throughout.
