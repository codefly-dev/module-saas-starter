-- The policy log's local half is withdrawn.
--
-- A narrowing of authority (a member removed, a grant, share, key, principal
-- or delegation revoked, an installation narrowed or uninstalled, a withdrawn
-- solution target closed, a ceiling lowered) now applies directly, recorded in
-- accounts' audit journal in the same transaction (owner decision,
-- 2026-10-27). Nothing appends to an external log first, so no receipt is
-- written and no cursor advances: both relations held only that protocol's
-- state, and nothing reads them any more.

DROP TABLE public.policy_log_cursor;
DROP TABLE public.policy_log_commits;
