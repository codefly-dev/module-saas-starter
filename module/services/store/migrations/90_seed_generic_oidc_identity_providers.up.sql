-- user_identities.provider is a foreign key into identity_providers, so every
-- selectable IDENTITY_PROVIDER must exist here or the first login fails with a
-- FK violation (SQLSTATE 23503) — long after startup succeeded. Seed the OpenID
-- Connect providers the auth stack can select: the generic `oidc` name, the
-- Auth0 preset (previously unseeded), and the concrete enterprise examples the
-- generic path documents. A deployment that configures another generic provider
-- name registers it the same way, via its own migration.
INSERT INTO identity_providers (provider_id, name) VALUES
    ('oidc',  'OpenID Connect'),
    ('auth0', 'Auth0'),
    ('okta',  'Okta'),
    ('ping',  'PingFederate')
ON CONFLICT (provider_id) DO NOTHING;
