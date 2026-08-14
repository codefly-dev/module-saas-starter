-- Register "header-jwt" as a known identity provider so the gateway-pre-auth
-- validator can insert user_identities rows without a FK violation
-- (user_identities.provider REFERENCES identity_providers.provider_id). This
-- mirrors the earlier "dev" seed. Deployments that set IDENTITY_PROVIDER_NAME to
-- a custom value must seed that id the same way.
INSERT INTO identity_providers (provider_id, name) VALUES ('header-jwt', 'Header JWT')
ON CONFLICT (provider_id) DO NOTHING;
