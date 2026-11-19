-- Restores both relations, empty, exactly as the baseline defines them.
SET LOCAL lock_timeout = '15s';
CREATE TABLE public.feature_flags (
 id uuid DEFAULT gen_random_uuid() NOT NULL,
 name text NOT NULL,
 description text,
 enabled boolean DEFAULT false NOT NULL,
 rollout_percent integer DEFAULT 0,
 target_org_ids uuid[] DEFAULT '{}'::uuid[],
 created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
 updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
 CONSTRAINT feature_flags_rollout_percent_check CHECK (((rollout_percent >= 0) AND (rollout_percent <= 100))),
 CONSTRAINT feature_flags_pkey PRIMARY KEY (id),
 CONSTRAINT feature_flags_name_key UNIQUE (name)
);
CREATE INDEX idx_feature_flags_target_orgs ON public.feature_flags USING gin (target_org_ids);
REVOKE ALL ON public.feature_flags FROM PUBLIC;
GRANT SELECT ON TABLE public.feature_flags TO app_tenant;
GRANT SELECT ON TABLE public.feature_flags TO app_control_plane;

CREATE TABLE public.magic_links (
 id uuid DEFAULT gen_random_uuid() NOT NULL,
 email text NOT NULL,
 token_hash text NOT NULL,
 expires_at timestamp with time zone NOT NULL,
 used_at timestamp with time zone,
 created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
 CONSTRAINT magic_links_pkey PRIMARY KEY (id),
 CONSTRAINT magic_links_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX idx_magic_links_email ON public.magic_links USING btree (email);
CREATE INDEX idx_magic_links_token_hash ON public.magic_links USING btree (token_hash);
ALTER TABLE public.magic_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.magic_links FORCE ROW LEVEL SECURITY;
CREATE POLICY magic_links_system ON public.magic_links USING (false) WITH CHECK (false);
CREATE POLICY app_control_plane_explicit_rows ON public.magic_links TO app_control_plane
 USING ((CURRENT_USER = 'app_control_plane'::name)) WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));
REVOKE ALL ON public.magic_links FROM PUBLIC;
GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE public.magic_links TO app_control_plane;
