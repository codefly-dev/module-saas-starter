-- RFC 8707 resource indicators on the client sign-in flow (issue #1003).
--
-- An MCP client asks for a token for one named resource — a solution's MCP
-- endpoint — and the token it gets carries that URL as a second audience, so it
-- is refused at any other solution's surface. Two columns carry that binding:
-- the authorization code records what the person approved, and the session
-- records what was minted, so every refresh rotation reissues the same audience
-- instead of re-reading it from a request the client controls.
--
-- Both are nullable with no default. Every session and every code that exists
-- today named no resource, which is exactly what NULL means here, so there is
-- nothing to backfill and no invariant that pre-existing rows now break.

-- The resource indicator this session's access tokens are bound to. NULL is a
-- session with no resource binding: the host's own web session, and every
-- registered-client session minted before resource indicators existed.
ALTER TABLE public.sessions ADD COLUMN resource text;

-- What the person approved, read back at redemption. A token request may repeat
-- `resource` (RFC 8707 §2.2) and it must match this; it can never set it.
ALTER TABLE public.client_authorization_codes ADD COLUMN resource text;

-- The scope granted, echoed on the token response (RFC 6749 §5.1).
ALTER TABLE public.client_authorization_codes ADD COLUMN scope text;
