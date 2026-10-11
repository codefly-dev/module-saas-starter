-- The module ENVELOPE: the authority ceiling a platform super_admin writes at
-- runtime, and the one dynamic record a static delivery is held against.
--
-- It replaces a file read once at boot from the trust anchor. A file is a
-- deploy-time fact, so narrowing a module meant a configuration change and an
-- accounts restart — the opposite of what the envelope is: an interactive,
-- audited act that takes effect at the next check, with no redeploy.
--
-- APPEND-ONLY AND REVISIONED. Every write is a new row at the next revision,
-- and the current envelope is the highest revision. Nothing updates or deletes
-- a row: the history of who widened or narrowed which module, and when, is the
-- record a restore and an audit both read, and a ceiling that can be edited in
-- place is a ceiling whose history can be rewritten. Revisions are contiguous
-- from 1 — the writer inserts `current + 1` only while `current` is what its
-- caller read — so two administrators writing at once cannot both win: the
-- second collides on the primary key and is told to re-read.
--
-- The document is the whole ceiling at that revision, not a delta: per module
-- principal the authority units this host admits, the scope ceilings, and the
-- grant ceiling (tenant, cross-tenant, queues, namespaces, audiences, artifact
-- policies). Its own `revision` must equal the row's, so a document cannot
-- claim one revision while being stored at another.

CREATE TABLE public.module_envelopes (
    revision bigint NOT NULL,
    document jsonb NOT NULL,
    -- The platform administrator who wrote this revision: a user id, kept
    -- without a foreign key so the record of who changed a ceiling outlives
    -- the account that changed it.
    written_by uuid NOT NULL,
    written_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,

    CONSTRAINT module_envelopes_revision_check CHECK ((revision > 0)),
    CONSTRAINT module_envelopes_document_object CHECK ((jsonb_typeof(document) = 'object'::text)),
    CONSTRAINT module_envelopes_document_revision CHECK (((document ->> 'revision'::text) = (revision)::text))
);

ALTER TABLE ONLY public.module_envelopes
    ADD CONSTRAINT module_envelopes_pkey PRIMARY KEY (revision);

-- Control-plane only, like every other platform relation: no tenant column, no
-- row-level security, and request traffic has no access. SELECT and INSERT only
-- — no UPDATE and no DELETE, because a ceiling whose history can be edited is
-- not a record of what was allowed. A correction is a new revision.
GRANT SELECT,INSERT ON TABLE public.module_envelopes TO app_control_plane;
