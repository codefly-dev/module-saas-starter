-- Make datasource_sources provider-modular (issue #468). Until now the table
-- was GitHub-shaped: the provider CHECK admitted only 'github' and the
-- GitHub-specific repo column was mandatory. The generic "API with credentials"
-- connector is the second provider and has no repo; its non-secret config
-- (base URL, resource path, credential kind + header) lives in a single JSONB
-- column so a new provider adds a config shape, not a set of columns.
--
-- credential_secret_ref / webhook_secret_ref keep holding the SecretCipher
-- envelopes for every provider — the API connector's bearer/basic/header
-- credential and any webhook secret are stored exactly as GitHub's are.

ALTER TABLE datasource_sources
    DROP CONSTRAINT datasource_sources_provider_check,
    ADD  CONSTRAINT datasource_sources_provider_check
             CHECK (provider IN ('github', 'api')),
    ALTER COLUMN repo DROP NOT NULL,
    ADD  COLUMN config JSONB;
