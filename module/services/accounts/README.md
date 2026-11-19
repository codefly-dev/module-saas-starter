# Goal is for customers to be ready to ship micro-SaaS

It needs to stay as generic as possible.

Hierarchy

Organization
:has_many Team

Team
:has_many User

User

Customer
:oneof Organization/Team/User


Permissions

RBAC model

### Incomplete suspension containment

Suspension commits the user status and session revocations before publishing
bearer-revocation markers. A failed marker write reports
`ErrSessionEndIncomplete`; it does not roll back suspension or report complete
containment. Retrying suspension includes previously invalidated, unexpired
sessions so missing session and family markers can be published again.
The reconciliation sweep also republishes markers from durable revoked,
unexpired session rows on startup and every minute. Failed publication is
retried after restart as well. Session cleanup retains rows until expiry.
