-- VerifyMagicLink resolves a verified link through the standard JIT pipeline
-- with a synthesized `magic_link` provider and the email as subject. That
-- provider was never seeded, so user_identities.provider — a foreign key into
-- identity_providers — rejected the insert with SQLSTATE 23503 and provisioning
-- could never succeed. The lookup that precedes it matches on
-- (provider, provider_id), so the flow could not recover on a later attempt
-- either: the row it searched for was the one provisioning had failed to write.
INSERT INTO public.identity_providers (provider_id, name) VALUES
    ('magic_link', 'Magic Link')
ON CONFLICT (provider_id) DO NOTHING;
