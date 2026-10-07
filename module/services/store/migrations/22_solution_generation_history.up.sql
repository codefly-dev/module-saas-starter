-- The generation history: one row per generation this host DECIDED about.
--
-- The audit trail the owner asked for after the registration-event incident, and
-- the source for the Catalogue's "last N generations per component with
-- applied/refused/tombstoned status".
--
-- It cannot be derived from anything that already exists.
-- public.solution_host_bindings holds exactly ONE desired and ONE applied
-- generation, and its pending_reason is the CURRENT refusal, overwritten on every
-- pass. So a refusal that is superseded leaves no trace, and the sequence of
-- generations a component moved through is unrecoverable the moment the next one
-- arrives.
--
-- It is also why this lands before the screen that reads it rather than with it:
-- a history relation added later starts EMPTY. If the trail is to cover the period
-- from the moment the new host exists, the recording has to begin when the
-- reconciler starts deciding, not when the Catalogue ships.
--
-- Append-only by grant: the control plane may SELECT and INSERT and nothing else.
-- A decision is a fact about the past, and a trail whose rows can be edited is not
-- a trail. Retention is a separate concern and will drop whole ranges rather than
-- rewrite rows.

CREATE TABLE public.solution_generation_history (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- Which component this decision was about. Not a foreign key: the trail
    -- outlives the binding row, and a decision about a binding whose first
    -- generation was refused has no row to reference.
    binding_id text NOT NULL,
    -- The target this decision applied into, when it reached one. Absent for a
    -- refusal, and for a tombstone of a binding that never applied.
    target_id uuid,
    generation bigint NOT NULL,
    -- The canonical digest of the document decided about, so two decisions at one
    -- generation number are distinguishable — which is the rewritten-generation
    -- case the host alarms on.
    digest text NOT NULL,
    -- What the host decided. `current` is recorded deliberately: a re-read that
    -- changed nothing is still evidence the document was present and unchanged at
    -- that moment, which is what distinguishes "delivery stopped" from "delivery
    -- kept saying the same thing".
    decision text NOT NULL,
    -- Why, for a refusal. Carries the same text the binding's pending_reason
    -- showed at the time, so a superseded refusal survives.
    reason text,
    -- The release the generation declared, for reading the trail without joining
    -- back to a document that may since have been replaced.
    release text,
    -- The registry revision an apply produced, so a reader can line this trail up
    -- against the registry's own revision sequence.
    registry_revision bigint,
    decided_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,

    CONSTRAINT solution_generation_history_binding_id_check
        CHECK ((binding_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,126}[A-Za-z0-9]$'::text)),
    CONSTRAINT solution_generation_history_generation_check CHECK ((generation > 0)),
    CONSTRAINT solution_generation_history_digest_check
        CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT solution_generation_history_decision_check
        CHECK ((decision = ANY (ARRAY['applied'::text, 'current'::text, 'refused'::text, 'tombstoned'::text]))),
    -- A refusal explains itself, and nothing else carries a reason. The
    -- comparison of two IS NULL tests is never NULL, so this cannot pass
    -- vacuously.
    CONSTRAINT solution_generation_history_reason_explains_refusal
        CHECK (((decision = 'refused'::text) = (reason IS NOT NULL)))
);

ALTER TABLE ONLY public.solution_generation_history
    ADD CONSTRAINT solution_generation_history_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.solution_generation_history
    ADD CONSTRAINT solution_generation_history_target_id_fkey
        FOREIGN KEY (target_id) REFERENCES public.solution_targets(id);

-- The Catalogue's read: the last N decisions for one component, newest first.
CREATE INDEX solution_generation_history_component
    ON public.solution_generation_history USING btree (binding_id, decided_at DESC);

-- One decision per (binding, generation, digest, decision): a pass that re-reads
-- an unchanged document must not append a row per pass, or a poll every thirty
-- seconds becomes the registration-event incident again in a different table.
-- A genuinely new decision about the same generation differs in digest (a rewrite)
-- or in decision (refused then applied), so it still records.
CREATE UNIQUE INDEX solution_generation_history_one_per_decision
    ON public.solution_generation_history USING btree (binding_id, generation, digest, decision);

-- Append-only: SELECT and INSERT, never UPDATE or DELETE. A decision is a fact
-- about the past.
GRANT SELECT,INSERT ON TABLE public.solution_generation_history TO app_control_plane;
