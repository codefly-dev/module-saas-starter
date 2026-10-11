-- The delivery inbox: every signed document this host has been handed, kept.
--
-- This replaces a directory as the reconciler's input, and the reason is a
-- defect rather than a preference. A mount is the CURRENT desired set and
-- nothing else, so a document that arrived, was recorded as desired, failed to
-- apply, and then disappeared from the mount was never retried — the host had
-- recorded that delivery wanted something and had no way to want it again. The
-- persisted representation also dropped the carrier, so a restore could not
-- re-verify what it had accepted; it could only trust its own earlier judgement.
--
-- So the host persists the CARRIER, verbatim, on receipt: the canonical payload
-- and the bundle that attests it. Everything else in this table is derived from
-- those bytes at receipt time and kept so a reader need not re-verify to answer
-- an operator's question.
--
-- Removal is still a tombstone GENERATION, never the absence of a row. Nothing
-- deletes from this table, which is what makes "delivery stopped talking" and
-- "delivery said remove it" different facts — the distinction a directory
-- cannot express at all.

CREATE TABLE public.solution_delivery_documents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,

    -- Which half of the lifecycle this document is. The two have different
    -- authorised writers and different identities, so they are never
    -- interchangeable and the kind is part of every lookup.
    kind text NOT NULL,

    -- The document's own identity: a binding id for presence, an authority id
    -- for authority. Opaque here on purpose — the host does not parse it, and an
    -- earlier authority shape (`<binding>:<service>`) was replaced precisely
    -- because something had parsed it.
    document_id text NOT NULL,

    -- Strictly monotonic per (kind, document_id), starting at 1.
    generation bigint NOT NULL,

    -- sha256 of the canonical payload. The idempotency key together with the
    -- generation: a re-POST of the same triple is a replay, while the SAME
    -- generation with a different hash is a rewrite of a delivered generation
    -- and is refused rather than stored.
    content_hash text NOT NULL,

    -- The carrier, verbatim. `payload` is the canonical JSON the signature
    -- covers; `bundle` is the attestation over exactly those bytes. Both are
    -- kept so a restore can re-verify rather than trust the row.
    payload bytea NOT NULL,
    bundle bytea NOT NULL,

    -- The attested signer identity the verifier returned, and the ownership
    -- domain the document asserts. Recorded at receipt so an operator can see
    -- who delivered what without re-running verification, and so a later
    -- narrowing of the policy is visible as a disagreement with history rather
    -- than as a silent change in what the host would accept today.
    signer_identity text NOT NULL,
    ownership_domain text NOT NULL,

    received_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,

    CONSTRAINT solution_delivery_documents_kind_check
        CHECK ((kind = ANY (ARRAY['presence'::text, 'authority'::text]))),
    CONSTRAINT solution_delivery_documents_document_id_check
        CHECK ((document_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,190}[A-Za-z0-9]$'::text)),
    CONSTRAINT solution_delivery_documents_generation_check
        CHECK ((generation > 0)),
    -- `sha256:` plus exactly 64 lowercase hex. A readable stand-in for a digest
    -- is not a digest, and this is the constraint that says so.
    CONSTRAINT solution_delivery_documents_content_hash_check
        CHECK ((content_hash ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT solution_delivery_documents_signer_check
        CHECK ((length(signer_identity) BETWEEN 1 AND 512)),
    CONSTRAINT solution_delivery_documents_domain_check
        CHECK ((length(ownership_domain) BETWEEN 1 AND 128)),
    -- A carrier with no bytes on either side is not a carrier. Empty would
    -- otherwise satisfy every check above and store as a document that can
    -- never be re-verified.
    CONSTRAINT solution_delivery_documents_payload_present
        CHECK ((octet_length(payload) > 0)),
    CONSTRAINT solution_delivery_documents_bundle_present
        CHECK ((octet_length(bundle) > 0))
);

ALTER TABLE ONLY public.solution_delivery_documents
    ADD CONSTRAINT solution_delivery_documents_pkey PRIMARY KEY (id);

-- ONE row per (kind, document_id, generation). This is what makes a rewrite of a
-- delivered generation detectable rather than merely discouraged: a second POST
-- at the same generation with different bytes collides here, and the handler
-- turns that collision into the conflict an operator needs to see instead of
-- overwriting history.
CREATE UNIQUE INDEX solution_delivery_documents_generation_unique
    ON public.solution_delivery_documents USING btree (kind, document_id, generation);

-- The reconciler's read: the newest generation per document, per kind.
CREATE INDEX solution_delivery_documents_newest
    ON public.solution_delivery_documents USING btree (kind, document_id, generation DESC);

CREATE INDEX solution_delivery_documents_received
    ON public.solution_delivery_documents USING btree (received_at DESC);

-- Control-plane only, like every other presence relation: no tenant column, no
-- row-level security, and request traffic has no access. INSERT and SELECT only
-- — no UPDATE and no DELETE, because an inbox that can be edited is not a record
-- of what was received. A correction is a new generation.
GRANT SELECT,INSERT ON TABLE public.solution_delivery_documents TO app_control_plane;
