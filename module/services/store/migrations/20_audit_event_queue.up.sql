-- The transactional audit queue (ADR 0009). Under a swap value of AUDIT_SINK a
-- warehouse is the audit store of record, and no external system can join the
-- transaction a change commits in. So the emitter writes each event here, on
-- that transaction — beside the domain event webhook delivery fans out from,
-- exactly where it would have written the audit_events row — and the accounts
-- relay delivers it to the warehouse and the locked archive afterwards. A row
-- lives here only until both have acknowledged it; then the relay deletes it.
--
-- It is a table of its own so that audit_events stays exactly as it is: its
-- append-only triggers, its SELECT/INSERT-only grants and its policies are not
-- touched, and under a swap value it simply receives no new rows. Under the
-- default (postgres) and the tee (both) nothing writes this table.
CREATE TABLE public.audit_event_queue (
    -- Insertion order. A batch is read oldest first, but delivery order is not
    -- guaranteed: see xact_id below. Nothing downstream depends on it, because
    -- both stores key a record by event id and every read orders by event time.
    seq bigint NOT NULL,
    -- The writing transaction. The relay reads a row only once every
    -- transaction older than the oldest one still running has finished, so it
    -- never reads a row whose transaction has not committed. The gate is
    -- transaction visibility, not sequence order: a row whose transaction took
    -- its id earlier but its sequence number later can be delivered ahead of a
    -- lower sequence number still to commit.
    xact_id xid8 DEFAULT pg_current_xact_id() NOT NULL,
    -- The event, column for column as audit_events keeps it.
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
    -- When the writing transaction began: how long a partial batch has waited.
    enqueued_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT audit_event_queue_actor_type_check CHECK ((actor_type = ANY (ARRAY['user'::text, 'api_key'::text, 'system'::text, 'agent'::text]))),
    CONSTRAINT audit_event_queue_event_type_format CHECK ((event_type ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2,}$'::text)),
    CONSTRAINT audit_event_queue_impersonation_identity_complete CHECK (((is_impersonated AND (impersonated_by IS NOT NULL)) OR ((NOT is_impersonated) AND (impersonated_by IS NULL))))
);

ALTER TABLE public.audit_event_queue ALTER COLUMN seq ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.audit_event_queue_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

ALTER TABLE ONLY public.audit_event_queue
    ADD CONSTRAINT audit_event_queue_pkey PRIMARY KEY (seq);

-- A queued event names a registered type, as an audit_events row must.
ALTER TABLE ONLY public.audit_event_queue
    ADD CONSTRAINT audit_event_queue_event_type_fkey FOREIGN KEY (event_type) REFERENCES public.audit_event_types(name);

ALTER TABLE public.audit_event_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE ONLY public.audit_event_queue FORCE ROW LEVEL SECURITY;

-- The tenant policy is audit_events_tenant's, expression for expression: a
-- request writes only its own organization's event, or an organization-less
-- one whose actor is the bound user.
CREATE POLICY audit_event_queue_tenant ON public.audit_event_queue
    USING (((org_id IS NOT NULL) AND ((org_id)::text = current_setting('app.current_org_id'::text, true))))
    WITH CHECK ((((org_id IS NOT NULL) AND ((org_id)::text = current_setting('app.current_org_id'::text, true))) OR ((org_id IS NULL) AND (actor_id IS NOT NULL) AND ((actor_id)::text = current_setting('app.current_user_id'::text, true)))));

-- The control plane writes platform events, which have no organization, and
-- every event of a mutation that runs on the control plane.
CREATE POLICY app_control_plane_explicit_rows ON public.audit_event_queue
    TO app_control_plane
    USING ((CURRENT_USER = 'app_control_plane'::name))
    WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));

-- The relay drains every organization's rows, on the job worker's pool.
CREATE POLICY app_job_worker_explicit_rows ON public.audit_event_queue
    TO app_job_worker
    USING ((CURRENT_USER = 'app_job_worker'::name))
    WITH CHECK ((CURRENT_USER = 'app_job_worker'::name));

-- Writers only insert: nothing that writes an event can read the queue back,
-- change a queued event or remove one. Only the relay reads and deletes, and
-- it never updates.
GRANT INSERT ON TABLE public.audit_event_queue TO app_tenant;
GRANT INSERT ON TABLE public.audit_event_queue TO app_control_plane;
GRANT SELECT,DELETE ON TABLE public.audit_event_queue TO app_job_worker;
