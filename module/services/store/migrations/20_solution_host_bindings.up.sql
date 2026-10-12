-- Declared solution presence: the host's durable record of the
-- SolutionHostBinding documents delivery hands it (issue #952).
--
-- Today a solution becomes present because its runtime registers itself. A
-- binding document is the opposite record: delivery declares which solution
-- runs on this host, at a generation, and the host reconciles towards it. The
-- document is desired state and carries no observation; what the host did about
-- it is applied state, and it lives here.
--
-- One row per binding ID, which identifies one deployment instance. The row is
-- never deleted: removal is a tombstone GENERATION, so applied_removed is true
-- and the generation stays on the record. That is what makes a replayed or
-- late-arriving older generation refusable after a removal, and what tells
-- "declared absent" apart from "no document was delivered".
--
-- The three column groups are the three facts an operator must be able to tell
-- apart. desired_* is the newest generation delivery has shown this host.
-- applied_* is the generation the host reconciled, and the registry record it
-- reconciled into. pending_reason is why a desired generation that is not the
-- applied one did not pass. Observed state is not here at all: it is the lease
-- and the endpoints on public.solution_registrations, reported by the runtime.
--
-- applied_document is kept, not just its digest, because the host must keep
-- serving the last generation that passed every check until a new one does. A
-- digest alone would tell it that the applied generation is not the desired one
-- and nothing about what it is still running.

CREATE TABLE public.solution_host_bindings (
    binding_id text NOT NULL,
    -- The coordinate the applied (or, before a first apply, the desired)
    -- document targets. A document naming another coordinate is refused before
    -- it reaches this table, so this records which host's record this is rather
    -- than deciding it.
    host_coordinate text NOT NULL,
    host_component text NOT NULL,

    -- Desired: the newest generation read from the mount, whether or not it
    -- passed. Whole or absent.
    desired_generation bigint,
    desired_digest text,
    desired_document text,
    desired_seen_at timestamp with time zone,

    -- Applied: the generation this host reconciled. Whole or absent.
    applied_generation bigint,
    applied_digest text,
    applied_document text,
    applied_removed boolean DEFAULT false NOT NULL,
    -- The route aliases the applied generation holds. They are held until a
    -- later generation releases them or a tombstone withdraws them, which is
    -- exactly what core's solutionhost.Applied carries.
    applied_routes text[] DEFAULT '{}'::text[] NOT NULL,
    -- The public.solution_registrations key the applied generation reconciled
    -- into. A tombstone generation needs it to know which record to withdraw,
    -- because a removed generation declares no route to derive it from.
    applied_solution_id text,
    -- publisher/name@version of the applied generation, so a heartbeat that
    -- would replace the release can be refused against a declared record.
    applied_release text,
    -- The ownership domain the applied generation declared.
    --
    -- It is NOT decoration beside host_coordinate. The domain is what says who
    -- may change this record: core refuses a later generation for this binding
    -- that arrives under a different one, which is what stops an accepted
    -- delivery taking over a binding another delivery owns. A host that did not
    -- persist it would hand core an applied state with no domain, and core
    -- refuses THAT too — so the symptom is not a widened check, it is a
    -- reconciler that applies its first generation and then refuses the whole
    -- set on every pass afterwards.
    applied_domain text,
    applied_at timestamp with time zone,

    -- Pending: why the desired generation is not the applied one. Whole or
    -- absent, and cleared by the pass that applies the desired generation.
    pending_reason text,
    pending_since timestamp with time zone,

    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,

    -- core's bindingPattern, which admits a ULID, a UUID and a dotted slug and
    -- excludes anything that would need escaping in a path or a label.
    CONSTRAINT solution_host_bindings_binding_id_check
        CHECK ((binding_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,126}[A-Za-z0-9]$'::text)),
    -- "base" is composition.ValidateCollisions' reserved-namespace exemption
    -- sentinel and core refuses it as a binding ID; refusing it here too stops a
    -- hand-written row from inheriting the exemption.
    CONSTRAINT solution_host_bindings_binding_id_not_reserved
        CHECK ((binding_id <> 'base'::text)),
    CONSTRAINT solution_host_bindings_host_coordinate_check
        CHECK ((host_coordinate <> ''::text)),
    CONSTRAINT solution_host_bindings_host_component_check
        CHECK ((host_component <> ''::text)),
    CONSTRAINT solution_host_bindings_desired_generation_check
        CHECK ((desired_generation > 0)),
    CONSTRAINT solution_host_bindings_applied_generation_check
        CHECK ((applied_generation > 0)),
    CONSTRAINT solution_host_bindings_desired_digest_check
        CHECK ((desired_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT solution_host_bindings_applied_digest_check
        CHECK ((applied_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    -- Whole or absent, both groups. A half-written generation would be a state
    -- no document ever described: a digest with no generation cannot be
    -- compared, and a generation with no digest cannot detect a rewrite.
    CONSTRAINT solution_host_bindings_desired_whole
        CHECK ((num_nonnulls(desired_generation, desired_digest, desired_document, desired_seen_at) = ANY (ARRAY[0, 4]))),
    CONSTRAINT solution_host_bindings_applied_whole
        CHECK ((num_nonnulls(applied_generation, applied_digest, applied_document, applied_solution_id, applied_release, applied_domain, applied_at) = ANY (ARRAY[0, 7]))),
    -- A domain is only meaningful on an applied generation, and an applied
    -- generation without one is the state core refuses. The whole-or-absent
    -- group above already pairs them; this names the non-empty rule, because
    -- '' passes num_nonnulls and is exactly the value core rejects.
    CONSTRAINT solution_host_bindings_applied_domain_check
        CHECK ((applied_domain <> ''::text)),
    -- A tombstone holds no alias; core asserts the same invariant when it reads
    -- a host's applied state back, and would refuse the whole pass rather than
    -- this one row.
    CONSTRAINT solution_host_bindings_tombstone_holds_no_route
        CHECK (((NOT applied_removed) OR (cardinality(applied_routes) = 0))),
    -- applied_removed is only meaningful for an applied generation.
    CONSTRAINT solution_host_bindings_removed_needs_applied
        CHECK (((NOT applied_removed) OR (applied_generation IS NOT NULL))),
    -- A pending refusal is whole or absent: a reason with no instant is not
    -- something an operator can act on, and the reverse explains nothing. Two
    -- IS NULL tests compared are never NULL, so this cannot pass vacuously.
    CONSTRAINT solution_host_bindings_pending_whole
        CHECK (((pending_reason IS NULL) = (pending_since IS NULL)))
);

ALTER TABLE ONLY public.solution_host_bindings
    ADD CONSTRAINT solution_host_bindings_pkey PRIMARY KEY (binding_id);

-- One live binding per registry key. Route aliases are unique within a host and
-- the resolved alias IS the registry key, so core's collision check already
-- refuses a second claimant before a generation applies; this index is what
-- makes that hold when two replicas reconcile the same pass concurrently.
-- Partial, because a tombstoned binding keeps the key it last applied so its
-- removal stays attributable, and that must not block a later binding claiming
-- the alias the tombstone released.
CREATE UNIQUE INDEX solution_host_bindings_live_solution
    ON public.solution_host_bindings USING btree (applied_solution_id)
    WHERE ((applied_solution_id IS NOT NULL) AND (NOT applied_removed));

-- The reconciler reads the whole table on every pass (one host holds tens of
-- bindings at most), so no listing index is needed. This one serves the
-- heartbeat path, which resolves "is this solution declared?" by key.
CREATE INDEX solution_host_bindings_solution
    ON public.solution_host_bindings USING btree (applied_solution_id)
    WHERE (applied_solution_id IS NOT NULL);

-- Control-plane only, exactly like public.solution_registrations: no tenant
-- column, no row-level security, and request traffic has no direct access.
GRANT SELECT,INSERT,UPDATE ON TABLE public.solution_host_bindings TO app_control_plane;

-- The registration record learns which binding declared it.
--
-- This is the link the mixed window is enforced through. Until the runtimes
-- stop self-registering, both paths write public.solution_registrations: the
-- reconciler applies declared presence, and a runtime heartbeats observations.
-- A heartbeat for a DECLARED record may only refresh what the declaration does
-- not own — the lease, the manifest, the upstream address. It may not create
-- presence, replace the release, repoint a route or erase a tombstone.
--
-- The three columns live on the registration row rather than being looked up in
-- solution_host_bindings because the heartbeat path already row-locks this row:
-- reading the declaration from the locked row is what serializes a heartbeat
-- against a concurrent apply. A second table read would leave a window in which
-- a heartbeat decided it was undeclared and then wrote.
--
-- NULL declared_binding_id is an undeclared record, and every existing row is
-- one: the heartbeat path for those is unchanged, which is what lets this land
-- before any runtime changes.
ALTER TABLE public.solution_registrations
    ADD COLUMN declared_binding_id text,
    ADD COLUMN declared_generation bigint,
    ADD COLUMN declared_release text;

ALTER TABLE public.solution_registrations
    ADD CONSTRAINT solution_registrations_declared_whole
        CHECK ((num_nonnulls(declared_binding_id, declared_generation, declared_release) = ANY (ARRAY[0, 3]))),
    ADD CONSTRAINT solution_registrations_declared_generation_check
        CHECK ((declared_generation > 0)),
    ADD CONSTRAINT solution_registrations_declared_binding_id_check
        CHECK ((declared_binding_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,126}[A-Za-z0-9]$'::text));

-- A registration is declared by at most one binding, and a binding declares at
-- most one LIVE registration.
--
-- Partial on the tombstone, not just on NULL. A binding whose generation renames
-- its route withdraws the record it held and declares the one it moved to, in a
-- single transaction, and the withdrawal deliberately KEEPS its declaration — that
-- is what makes the removal hold against a late heartbeat. So that binding names
-- two rows for an instant and forever after: one live, and one tombstoned for
-- every alias it has ever released. Only the live one may be unique.
CREATE UNIQUE INDEX solution_registrations_declared_binding
    ON public.solution_registrations USING btree (declared_binding_id)
    WHERE ((declared_binding_id IS NOT NULL) AND (tombstoned_at IS NULL));
