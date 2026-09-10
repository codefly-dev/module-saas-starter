-- Durable, versioned registry of runtime-registered solutions (issue #534).
--
-- A solution is an independently deployed component the host has no build-time
-- knowledge of. It self-registers two halves: a frontend half (the
-- Module-Federation manifest the host renders) and a backend half (the upstream
-- the gateway proxies to). Those halves used to live in two process-local maps,
-- so a restart lost them, a replica behind a load balancer never saw them, and
-- one half could be live while the other was missing.
--
-- This relation is the single authority both halves are written to and read
-- back from. It is a platform relation with no tenant column: registrations are
-- deployment topology, not customer data, so there is no RLS to write and the
-- request role gets no access at all. Only the control plane reads or writes it.
--
-- Two facts are deliberately separate:
--
--   * durable installation intent — the row exists and is not tombstoned;
--   * ephemeral endpoint liveness — the per-half lease, renewed by the
--     registrant and allowed to expire when its process goes away.
--
-- Deleting a registration tombstones it rather than removing the row, and
-- clears both halves in the same statement: a delayed heartbeat from a retiring
-- deployment then finds a tombstone at a revision it does not hold and cannot
-- resurrect the registration, and a reader that ignored the tombstone still has
-- no endpoint to route to.

CREATE SEQUENCE public.solution_registry_revision_sequence
    AS BIGINT
    MINVALUE 1
    START WITH 1
    NO CYCLE;

CREATE TABLE public.solution_registrations (
    -- Stable solution identity, chosen by the publisher and used as the routing
    -- key on both surfaces (/s/<id> and /solutions/<id>/*).
    solution_id TEXT PRIMARY KEY CHECK (solution_id <> ''),
    -- Owner of record for the registration. First claim binds it; a later
    -- registration naming a different publisher is refused rather than
    -- overwriting, so one publisher cannot take over another's identity.
    publisher TEXT NOT NULL CHECK (publisher <> ''),
    -- Registry-wide monotonic revision of the record as a whole. Every write
    -- draws a fresh value from the shared sequence, so revisions are comparable
    -- across records and a consumer can tell one snapshot from a later one.
    revision BIGINT NOT NULL CHECK (revision > 0),
    tombstoned_at TIMESTAMPTZ,

    -- Frontend half. manifest is the document the frontend validated before
    -- storing it; this relation persists it verbatim and never reinterprets it.
    frontend_revision BIGINT CHECK (frontend_revision > 0),
    frontend_manifest JSONB,
    frontend_contract_version TEXT,
    frontend_lease_expires_at TIMESTAMPTZ,

    -- Backend half.
    backend_revision BIGINT CHECK (backend_revision > 0),
    backend_upstream TEXT,
    backend_service_alias TEXT,
    backend_contract_version TEXT,
    backend_lease_expires_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- A half is present as a whole or absent as a whole. A partially written
    -- half would read as live registration state with a missing endpoint, which
    -- is exactly the incoherence this table exists to prevent.
    CONSTRAINT solution_registrations_frontend_half_whole CHECK (
        num_nonnulls(frontend_revision, frontend_manifest, frontend_lease_expires_at) IN (0, 3)
    ),
    CONSTRAINT solution_registrations_backend_half_whole CHECK (
        num_nonnulls(backend_revision, backend_upstream, backend_service_alias, backend_lease_expires_at) IN (0, 4)
    ),
    -- A tombstone carries no endpoints.
    CONSTRAINT solution_registrations_tombstone_is_empty CHECK (
        tombstoned_at IS NULL
        OR num_nonnulls(frontend_revision, backend_revision) = 0
    )
);

REVOKE ALL PRIVILEGES ON public.solution_registrations FROM PUBLIC, app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON public.solution_registrations TO app_control_plane;

REVOKE ALL PRIVILEGES ON SEQUENCE public.solution_registry_revision_sequence
    FROM PUBLIC, app_tenant;
GRANT USAGE, SELECT ON SEQUENCE public.solution_registry_revision_sequence
    TO app_control_plane;
