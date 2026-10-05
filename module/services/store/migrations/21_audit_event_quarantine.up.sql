-- The audit relay's quarantine (ADR 0009). The relay delivers the transactional
-- queue to the warehouse and the archive in batches, and splits a batch the
-- warehouse refuses to find the row it cannot take. A row the warehouse refuses
-- on its own, while it accepts others, would otherwise sit at the head of the
-- queue forever and hold every organization's events behind it; the relay moves
-- it here instead, whole, together with why.
--
-- Nothing deletes a quarantined row: not the relay, not retention. The row was
-- committed with a change to the platform and is evidence of it. An operator
-- replays or resolves quarantined rows; replay is not built yet, and until it
-- is the table is how the rows are kept and counted (saas.audit_queue.quarantined).
--
-- It mirrors audit_event_queue column for column, so a row can be put back into
-- the queue as it was. It has no foreign key to the event-type registry, unlike
-- the queue: a quarantined row must outlive the registry entry it was written
-- under, which a retired type's removal would otherwise be blocked by.
CREATE TABLE public.audit_event_quarantine (
    -- The queue's sequence number, kept: it is what the relay's log and the
    -- move name the row by.
    seq bigint NOT NULL,
    xact_id xid8 NOT NULL,
    id uuid NOT NULL,
    event_type text NOT NULL,
    schema_version integer NOT NULL,
    actor_id uuid,
    actor_type text NOT NULL,
    resource text NOT NULL,
    resource_id text,
    org_id uuid,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    ip_address text,
    created_at timestamp with time zone NOT NULL,
    impersonated_by uuid,
    is_impersonated boolean DEFAULT false NOT NULL,
    client_id text,
    enqueued_at timestamp with time zone NOT NULL,
    -- When the relay set the row aside, and what the store said when it refused.
    quarantined_at timestamp with time zone DEFAULT now() NOT NULL,
    error text NOT NULL,
    CONSTRAINT audit_event_quarantine_actor_type_check CHECK ((actor_type = ANY (ARRAY['user'::text, 'api_key'::text, 'system'::text, 'agent'::text]))),
    CONSTRAINT audit_event_quarantine_event_type_format CHECK ((event_type ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2,}$'::text)),
    CONSTRAINT audit_event_quarantine_impersonation_identity_complete CHECK (((is_impersonated AND (impersonated_by IS NOT NULL)) OR ((NOT is_impersonated) AND (impersonated_by IS NULL)))),
    CONSTRAINT audit_event_quarantine_error_bounded CHECK ((char_length(error) <= 2000))
);

ALTER TABLE ONLY public.audit_event_quarantine
    ADD CONSTRAINT audit_event_quarantine_pkey PRIMARY KEY (seq);

ALTER TABLE public.audit_event_quarantine ENABLE ROW LEVEL SECURITY;
ALTER TABLE ONLY public.audit_event_quarantine FORCE ROW LEVEL SECURITY;

-- Only the relay's role sees or writes the quarantine. Request traffic holds no
-- grant on it. The tenant policy is the one every tenant-scoped relation carries
-- (membership_integrity_findings is the precedent for a relation no request can
-- reach): it keeps the table inside the tenant boundary, so a grant added later
-- by mistake would still show an organization nothing but its own rows, and only
-- for reading. A platform event, with no organization, matches no tenant.
CREATE POLICY audit_event_quarantine_tenant ON public.audit_event_quarantine
    FOR SELECT
    USING (((org_id)::text = current_setting('app.current_org_id'::text, true)));

CREATE POLICY app_job_worker_explicit_rows ON public.audit_event_quarantine
    TO app_job_worker
    USING ((CURRENT_USER = 'app_job_worker'::name))
    WITH CHECK ((CURRENT_USER = 'app_job_worker'::name));

-- The relay moves rows in and counts them. It cannot change or remove one.
GRANT SELECT,INSERT ON TABLE public.audit_event_quarantine TO app_job_worker;
