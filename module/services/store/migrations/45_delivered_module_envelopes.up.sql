-- A module envelope revision is written by a platform administrator OR
-- DELIVERED from the composition: signed by the cell's release key, handed over
-- through the signed delivery channel, admitted at a strictly increasing
-- revision. A delivered revision has no user behind it, so `written_by` becomes
-- nullable and the delivery is recorded instead: the pinned release key id that
-- signed it, the ownership domain it speaks for, the canonical payload the
-- signature covers and the DSSE envelope verbatim, so the row can be
-- re-verified and the next delivery admitted against it.
--
-- A row is exactly one of the two, by CHECK: a user's (written_by set, every
-- delivery column null) or a delivery's (written_by null, every delivery column
-- set). Still append-only — the grants stay SELECT and INSERT.
ALTER TABLE public.module_envelopes
    ALTER COLUMN written_by DROP NOT NULL,
    ADD COLUMN signer text CONSTRAINT module_envelopes_signer_check CHECK ((signer <> ''::text)),
    ADD COLUMN ownership_domain text CONSTRAINT module_envelopes_ownership_domain_check CHECK ((ownership_domain <> ''::text)),
    ADD COLUMN delivered_payload bytea,
    ADD COLUMN delivered_envelope jsonb CONSTRAINT module_envelopes_delivered_envelope_object_check CHECK ((jsonb_typeof(delivered_envelope) = 'object'::text)),
    ADD CONSTRAINT module_envelopes_writer_check CHECK ((
        (written_by IS NOT NULL AND signer IS NULL AND ownership_domain IS NULL
            AND delivered_payload IS NULL AND delivered_envelope IS NULL)
        OR
        (written_by IS NULL AND signer IS NOT NULL AND ownership_domain IS NOT NULL
            AND delivered_payload IS NOT NULL AND delivered_envelope IS NOT NULL)));
