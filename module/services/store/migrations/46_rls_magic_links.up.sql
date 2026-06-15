-- Phase 2C — RLS on magic_links. No tenant/user column (email + token_hash,
-- consumed pre-auth in passwordless login) so the only sound policy is the
-- audited System bypass: all access is system-level (SendMagicLink / VerifyMagicLink).

ALTER TABLE magic_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE magic_links FORCE  ROW LEVEL SECURITY;

CREATE POLICY magic_links_system ON magic_links
    USING (current_setting('app.bypass', true) = '1')
    WITH CHECK (current_setting('app.bypass', true) = '1');
