# Login vs Invite vs Signup — review and plan

Status: proposal. Nothing here is implemented yet.

The starter currently has **one** identity entry point that quietly does three
different jobs. This document reviews what exists, then proposes splitting it
into three explicit flows so that invite-only access, deterministic tenant
selection, and free-plan onboarding become configurable rather than emergent.

---

## 1. What exists today

Everything below is a single call:

```go
Resolve(ctx, claims, orgNameOnSignup) // pkg/auth/pg/resolver.go
```

`resolveInTx` runs three steps in one serializable transaction:

1. `upsertIdentity` — look up `(provider, subject)`; **JIT-provision** a `users`
   + `user_identities` row if absent.
2. `ensureOrg` — load a membership, or create an organization when
   `orgNameOnSignup != ""`.
3. `bootstrapOrLoadPlatformRole` — one-time `BOOTSTRAP_ADMIN_EMAIL` grant.

### Finding 1 — there is no "signup", only login with a side effect

Signup is expressed as *"a login where `orgNameOnSignup` happens to be
non-empty"*. There is no signup RPC, no signup policy, and no place to put one.
The distinction the product needs does not exist in the code.

### Finding 2 — invite-only is structurally impossible today

`AcceptInvitation` resolves the caller first:

```go
caller, err := s.getUserAsSelf(ctx, userID)
if caller == nil || !strings.EqualFold(caller.PrimaryEmail, inv.Email) {
    return nil, ErrInvitationEmailMismatch
}
```

The invitee must **already have a user row** to accept. The only way to get one
is open self-serve JIT provisioning at login. So closing self-serve signup also
blocks every invitee from ever accepting — the two are the same code path.

**This is the central blocker.** It is why invite-only cannot be delivered as a
config flag on top of the current design, and why this document exists.

The waitlist does not help: `InviteWaitlist` only sends email and creates no
users. It gates *intent*, not account creation.

### Finding 3 — tenant selection is implicit and admits the wrong answer

```sql
SELECT org_id, role FROM organization_members
WHERE user_id = $1 ORDER BY joined_at DESC LIMIT 1
```

"Most recent membership wins." The code's own comment concedes this is
provisional and wants an explicit `users.default_org_id`. For a user in several
orgs, the session tenant is whichever org they joined last — not a decision
anyone made.

### Finding 4 — WorkOS already tells us the org, and we discard it

`auth.Claims` carries `ProviderOrgID`, populated by the OIDC validator and the
WorkOS exchanger. **The resolver never reads it.** Meanwhile the SSO admin flow
stamps our organization id onto the WorkOS organization as `external_id`, so the
mapping in both directions already exists.

For an SSO'd organization this is the authoritative answer to "which org is this
person part of", and today it is thrown away in favour of Finding 3's heuristic.

### Finding 5 — orgless-but-authenticated is a real, unmodelled state

`ensureOrg` may return a nil org and still issue a token. That state is
legitimate, but nothing names it, so each caller improvises.

### Finding 6 — `email_verified` is asserted, not verified

The JIT insert hardcodes it:

```sql
INSERT INTO users (uuid, primary_email, status, email_verified)
VALUES ($1, $2, 'active', true)
```

`auth.Claims` has **no `EmailVerified` field**, so the IdP's own assertion is
never consulted. Because invitation acceptance authorizes on email equality, an
identity provider that permits unverified emails would let a user claim an
invitation addressed to someone else. Low severity while WorkOS is the only
configured provider; it becomes real the moment a second provider is enabled.

---

## 2. The three flows, stated explicitly

| Flow | Precondition | Creates user? | Creates org? | Gated by access policy? |
|---|---|---|---|---|
| **Login** | identity already exists | no | no | no |
| **Invite** | pending invitation matches the verified email | **yes** | no — joins the inviting org | no |
| **Signup** | no identity, no invitation | yes | optionally | **yes** |

The key property: **only Signup is gated.** Login and Invite stay open, which is
exactly what makes invite-only coherent — an invitee can always get an account,
while a stranger cannot.

---

## 3. Plan

### Step 1 — model intent at the resolver boundary

Replace the positional `orgNameOnSignup` with an explicit intent:

```go
type Intent interface{ isIntent() }

type LoginIntent  struct{}
type InviteIntent struct{ Token string }
type SignupIntent struct{ OrganizationName string }

Resolve(ctx, claims, intent Intent) (*auth.Identity, error)
```

`upsertIdentity` splits into `findIdentity` (never writes) and
`provisionIdentity` (writes, and only reachable from Invite and Signup).
A `LoginIntent` that finds no identity returns a typed
`ErrNoAccount` instead of silently creating one.

### Step 2 — break the invitation chicken-and-egg

Carry the invitation token through authentication so the user row is provisioned
**inside** the invite flow, bound to the invitation's email, in the same
transaction that adds the membership. `AcceptInvitation` then stops requiring a
pre-existing user.

Ordering matters: validate the token, confirm it is pending and unexpired, and
confirm the authenticated email matches the invitation **before** provisioning.

### Step 3 — introduce the access policy

New workspace configuration key, gating **signup only**:

```
IDENTITY_SIGNUP_MODE = open | invite | waitlist
```

- `open` — current behaviour; the default so nothing changes on upgrade.
- `invite` — signup requires a pending invitation; strangers are rejected.
- `waitlist` — signup requires an approved/invited waitlist entry, wiring the
  existing `WAITLIST_STATE_*` machine into access control for the first time.

Fail closed on an unrecognised value.

### Step 4 — make tenant selection deterministic

Add `users.default_org_id`, and resolve in this order:

1. `claims.ProviderOrgID` → `organizations.external_id` (authoritative for SSO)
2. `users.default_org_id`
3. the single membership, when there is exactly one
4. otherwise **no org** — return the orgless state and let the client choose

Delete "most recent membership wins". Name the orgless state so the frontend can
render an org chooser instead of guessing.

### Step 5 — propagate `email_verified`

Add `EmailVerified` to `auth.Claims`, populate it from the IdP, and persist it
rather than hardcoding `true`. Require a verified email before invitation
acceptance can authorize on email equality.

### Step 6 — free plan by default

Attach the free plan at organization creation so a new org is never plan-less,
instead of relying on an explicit `POST /v1/billing/free-plan`. Keep the
endpoint for explicit selection and idempotency.

---

## 4. Sequencing

Steps 1–2 are one unit: the split is what makes the invitation fix expressible.
Step 3 is small once they land. Steps 4–6 are independent and can ship in any
order.

| PR | Contents | Risk |
|---|---|---|
| 1 | Steps 1–2 — intent split, invite provisioning | Medium — touches every login |
| 2 | Step 3 — `IDENTITY_SIGNUP_MODE`, default `open` | Low |
| 3 | Step 4 — `default_org_id`, consume `ProviderOrgID` | Medium — migration |
| 4 | Steps 5–6 — `email_verified`, free plan default | Low |

## 5. Testing

These belong in the **pipeline** tier, against the real dependency graph — the
`pure` tier cannot prove them because the behaviour lives in Postgres
transactions and the gateway trust boundary.

- login with an unknown identity is rejected when `signup_mode=invite`
- an invited email signs up successfully under the same setting
- an invitation cannot be accepted from a different authenticated email
- an expired or already-accepted invitation fails closed
- a user in two orgs lands on `default_org_id`, not the most recent join
- an SSO user lands on the org matching `ProviderOrgID`
- a user in no org authenticates successfully and is reported as orgless
- a newly created org has the free plan attached
