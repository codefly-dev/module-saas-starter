-- Admit the two Tier-1 connectors added in issue #471 — the web/sitemap crawler
-- and the S3-compatible object-storage ("upload") provider — to the
-- datasource_sources provider CHECK. Migration 110 widened it from GitHub-only
-- to ('github', 'api'); these providers are the next entries on the same
-- provider-modular port. Their non-secret config rides in the existing JSONB
-- config column (crawler: sitemap_url/max_pages; upload: endpoint/region/bucket/
-- prefix/access_key_id/max_objects), so no new columns are needed — only the
-- constraint has to admit the new provider strings, or every AddSource for them
-- fails the CHECK at insert time.

ALTER TABLE datasource_sources
    DROP CONSTRAINT datasource_sources_provider_check,
    ADD  CONSTRAINT datasource_sources_provider_check
             CHECK (provider IN ('github', 'api', 'crawler', 'upload'));
