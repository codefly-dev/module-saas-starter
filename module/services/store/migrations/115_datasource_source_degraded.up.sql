-- Degraded datasource status (issue #501). A snapshot manifest larger than the
-- ingest payload cap (~960 KiB) fails terminally in the change-set compiler, but
-- the reconcile scheduler kept re-selecting the source every interval, so it
-- dead-lettered a reconcile forever with no visible status and no audit trail.
--
-- 'degraded' is a third status the compiler sets when it cannot make progress for
-- a structural reason an operator must resolve (today: an oversized manifest,
-- until the object-storage manifest seam lands). status_reason records why. The
-- reconcile sweep and its partial index already scope to status = 'active', so a
-- degraded source stops being re-selected the moment it is marked — the loop
-- ends without any schedule-bump special case. An operator resets it to 'active'.
--
-- Classification (DATABASE_AUTHORITY.md): datasource_sources stays a TENANT
-- relation with the same RLS policy and grants (migration 104) — a new value on
-- the existing status CHECK and a new nullable column on an existing table, both
-- covered by the table-level GRANTs. The leased compiler writes status_reason
-- through app_control_plane, which already holds UPDATE (migration 104).

ALTER TABLE datasource_sources
    DROP CONSTRAINT datasource_sources_status_check,
    ADD CONSTRAINT datasource_sources_status_check
        CHECK (status IN ('active', 'paused', 'degraded')),
    ADD COLUMN status_reason TEXT;
