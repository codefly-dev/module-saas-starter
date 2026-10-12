-- An immutable, never-reused identity for the thing an organisation installs.
--
-- Today `installations.solution_identifier` is free text, and the registry key a
-- solution is addressed by is its route alias, which is deliberately REUSABLE: a
-- tombstoned alias may be claimed by another binding
-- (`solution_host_bindings`, migration 20). Those two facts compose into a
-- transfer of consent:
--
--   1. Org A installs the binding behind alias `reports` and grants a team access.
--   2. That binding is withdrawn by a tombstone generation.
--   3. A DIFFERENT binding takes the alias `reports`.
--   4. The installation still names `reports`.
--   5. The new binding inherits org A's installation and its team exposure,
--      with no administrator having acted.
--
-- A foreign key to the registry would not fix this. It guarantees referential
-- integrity, not continuity of authorised identity, and pointing it at the alias
-- row would make the inheritance formal rather than accidental. Core states the
-- intended rule plainly: a second instance of a solution "gets its own binding ID
-- and inherits nothing from this one — no route, no generation history, no
-- installation".
--
-- So the thing an organisation installs is given its own identity here. A
-- solution target is one continuous period of one binding's presence on this
-- host. It is minted when a present generation is applied for a binding that has
-- no live target, and it is closed when that binding's tombstone generation is
-- applied. It is never reused: a binding that is withdrawn and later delivered
-- again gets a NEW target, because the tombstone ended the presence an
-- administrator consented to, and consenting again is a dynamic act.
--
-- The route alias lives here too, and may move between generations. It is a
-- property of the target, never its identity.

CREATE TABLE public.solution_targets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- The binding whose presence this target records. Not unique on its own: a
    -- binding withdrawn and re-presented has one row per period.
    binding_id text NOT NULL,
    -- What the host routes this target on, as of the applied generation. It moves
    -- with a generation that renames it, which is exactly why it is not the
    -- identity.
    solution_id text NOT NULL,
    -- The generation that opened this target, and the one that closed it. Closing
    -- is a tombstone generation, never a deletion.
    opened_generation bigint NOT NULL,
    closed_generation bigint,
    opened_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    closed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,

    CONSTRAINT solution_targets_binding_id_check
        CHECK ((binding_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,126}[A-Za-z0-9]$'::text)),
    CONSTRAINT solution_targets_binding_id_not_reserved
        CHECK ((binding_id <> 'base'::text)),
    CONSTRAINT solution_targets_solution_id_check
        CHECK ((solution_id ~ '^[a-z0-9](?:[a-z0-9_-]*[a-z0-9])?$'::text) AND (length(solution_id) <= 128)),
    CONSTRAINT solution_targets_opened_generation_check CHECK ((opened_generation > 0)),
    -- A close is whole or absent, and never precedes the open it closes. Two
    -- IS NULL tests compared are never NULL, so this cannot pass vacuously.
    CONSTRAINT solution_targets_close_whole
        CHECK (((closed_generation IS NULL) = (closed_at IS NULL))),
    CONSTRAINT solution_targets_close_follows_open
        CHECK (((closed_generation IS NULL) OR (closed_generation >= opened_generation)))
);

ALTER TABLE ONLY public.solution_targets
    ADD CONSTRAINT solution_targets_pkey PRIMARY KEY (id);

-- One live target per binding. A binding cannot be present twice at once, and
-- this is what makes "mint a target if the binding has no live one" a race-free
-- read-decide-write under the reconciler's row lock.
CREATE UNIQUE INDEX solution_targets_live_binding
    ON public.solution_targets USING btree (binding_id)
    WHERE (closed_generation IS NULL);

-- One live target per route alias, for the same reason migration 20's index
-- exists on the binding row: core refuses the collision before a generation
-- applies, and this is the durable backstop when two replicas reconcile one pass.
CREATE UNIQUE INDEX solution_targets_live_solution
    ON public.solution_targets USING btree (solution_id)
    WHERE (closed_generation IS NULL);

CREATE INDEX solution_targets_binding_history
    ON public.solution_targets USING btree (binding_id, opened_generation DESC);

-- Control-plane only, like the other presence relations: no tenant column, no
-- row-level security, and request traffic has no direct access. Never deleted —
-- a closed target is the evidence that an installation's consent ended.
GRANT SELECT,INSERT,UPDATE ON TABLE public.solution_targets TO app_control_plane;

-- `installations` is deliberately NOT touched here.
--
-- Pointing an installation at a target means dropping the free-text
-- `solution_identifier` it names today, and that column is read by the
-- installations store (a POSITIONAL scan), the `SolutionEntitlement` contract,
-- the gateway's entitlement JSON and the frontend projections that join on it.
-- Dropping it in the same migration as the table it would point at would land a
-- schema no deployed reader can serve, which is the one thing a cutover
-- migration must not do: it has to arrive with its readers, not before them.
--
-- So this migration establishes the identity and nothing else. The installation
-- side — the immutable `target_id`, the revocation of historical rows whose
-- target cannot be established, and dropping `solution_identifier` — lands with
-- the reader changes that make it serveable.
