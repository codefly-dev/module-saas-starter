# Solution event streams through the host

A consuming solution can return `text/event-stream` through the existing
`/api/solutions/:id/proxy/*` frontend route. The route still resolves a registered
solution and sends the request through the Accounts-protected auth gateway.
No direct service URL, new bearer format or product-specific route is introduced.

For GET observations the host forwards `Last-Event-ID` unchanged, with a 1,024-byte
UTF-8 bound and no control characters. Invalid cursors fail before registry or
upstream access. A cursor is a recovery hint, never an authorization credential.
The solution owns event identity, reset/gap interpretation and any replay policy.
Mutation requests never receive this header from the host.

The proxy passes the response body through as a stream without parsing or
accumulating events. Event-stream responses have `Cache-Control: no-store,
no-transform` and `X-Accel-Buffering: no`; internal headers and upstream cookies
remain excluded. Upstream fetches bypass caches, reject redirects and inherit
the browser request's abort signal. Closing an observation does not call a
solution mutation or imply execution cancellation. The host does not reconnect,
retry commands, fabricate a terminal event or reinterpret EOF as completion.
Authorization denials retain their upstream status and body.

The generic proxy tests cover real loopback HTTP delivery before EOF, heartbeat
and UTF-8 bytes, a subsequent same-path GET with its original cursor, abort and
response-body cancellation, redirect credential containment, denial without
retry, cursor bounds and separation from mutation requests. They use the actual
frontend route with controlled registry/SDK bindings and a local HTTP upstream;
they do not start Accounts or certify a deployed ingress.

From `services/frontend/code` after a locked dependency installation:

```sh
npx vitest run --project pure 'src/app/api/solutions/[id]/proxy/[...path]/__tests__'
npm run typecheck
```

A composition must still qualify its actual gateway, solution, ingress flushing
and compression, idle/connection limits, slow consumers and multi-tab load.
Authentication at connection establishment does not establish ongoing revocation
checks: the protected stream owner must enforce its current authority contract,
and the client must fence old-account observations during logout or identity
changes. Token deltas, durable event replay, execution renewal and reconnect
budgets are separate consuming-service contracts. These tests do not qualify
those contracts or production traffic.
