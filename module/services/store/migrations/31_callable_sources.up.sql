-- Source declarations and consumed OAuth state are tenant-owned host records.
ALTER TABLE public.datasource_sources ADD CONSTRAINT datasource_sources_org_identity UNIQUE (org_id,id);
CREATE TABLE public.datasource_source_operations (
 org_id uuid NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
 source_id uuid NOT NULL,
 name text NOT NULL CHECK (name ~ '^[a-z][a-z0-9_]{0,63}$'),
 method text NOT NULL CHECK (method IN ('GET','POST','PUT','PATCH','DELETE')),
 path text NOT NULL,
 description text NOT NULL DEFAULT '',
 query text[] NOT NULL DEFAULT '{}',
 input_schema jsonb NOT NULL CHECK (jsonb_typeof(input_schema) = 'object'),
 output_schema jsonb NOT NULL CHECK (jsonb_typeof(output_schema) = 'object'),
 effect text NOT NULL CHECK (effect IN ('READ_ONLY','MUTATION')),
 max_output_bytes integer NOT NULL CHECK (max_output_bytes BETWEEN 1 AND 65536),
 digest text NOT NULL CHECK (digest ~ '^sha256:[a-f0-9]{64}$'),
 FOREIGN KEY (org_id,source_id) REFERENCES public.datasource_sources(org_id,id) ON DELETE CASCADE,
 PRIMARY KEY (org_id,source_id,name)
);
ALTER TABLE public.datasource_source_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.datasource_source_operations FORCE ROW LEVEL SECURITY;
CREATE POLICY datasource_source_operations_tenant ON public.datasource_source_operations TO app_tenant
 USING (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid)
 WITH CHECK (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON public.datasource_source_operations TO app_tenant;

-- Only the state digest is persisted. PKCE verifiers are derived under the
-- host's existing signing key and are never delivered to the browser.
CREATE TABLE public.datasource_oauth_consumed_states (
 org_id uuid NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
 state_hash text NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY (org_id,state_hash)
);
ALTER TABLE public.datasource_oauth_consumed_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.datasource_oauth_consumed_states FORCE ROW LEVEL SECURITY;
CREATE POLICY datasource_oauth_consumed_states_tenant ON public.datasource_oauth_consumed_states TO app_tenant
 USING (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid)
 WITH CHECK (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);
GRANT SELECT, INSERT, DELETE ON public.datasource_oauth_consumed_states TO app_tenant;

-- An attempt is committed before egress. It is never aged out: forgetting an
-- unknown mutation would allow the same effect to execute a second time.
CREATE TABLE public.datasource_operation_attempts (
 org_id uuid NOT NULL REFERENCES public.organizations(id) ON DELETE CASCADE,
 effect_id text NOT NULL CHECK (length(effect_id) BETWEEN 1 AND 128),
 actor_id uuid NOT NULL,
 source_id uuid NOT NULL,
 operation text NOT NULL,
 declaration_digest text NOT NULL,
 request_digest bytea NOT NULL,
 PRIMARY KEY (org_id,effect_id)
);
ALTER TABLE public.datasource_operation_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.datasource_operation_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY datasource_operation_attempts_tenant ON public.datasource_operation_attempts TO app_tenant
 USING (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid)
 WITH CHECK (org_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);
GRANT SELECT, INSERT, DELETE ON public.datasource_operation_attempts TO app_tenant;

-- The SDK's receipt schema, tenant-filtered like every host-owned record.
CREATE TABLE public.codefly_effect_receipts (
 tenant text NOT NULL,
 effect_id text NOT NULL,
 method text NOT NULL,
 request_digest bytea NOT NULL,
 response bytea NOT NULL,
 committed_at timestamptz NOT NULL,
 PRIMARY KEY (tenant,effect_id,method)
);
CREATE INDEX codefly_effect_receipts_committed_at ON public.codefly_effect_receipts (tenant,committed_at);
ALTER TABLE public.codefly_effect_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.codefly_effect_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY codefly_effect_receipts_tenant ON public.codefly_effect_receipts TO app_tenant
 USING (tenant = NULLIF(current_setting('app.current_org_id', true), ''))
 WITH CHECK (tenant = NULLIF(current_setting('app.current_org_id', true), ''));
GRANT SELECT, INSERT, DELETE ON public.codefly_effect_receipts TO app_tenant;
