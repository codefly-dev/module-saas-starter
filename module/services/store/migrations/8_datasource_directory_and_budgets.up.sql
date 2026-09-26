-- The datasource directory and the connector budget.
--
-- A connector that translates a provider's per-item access lists can only name
-- host users and teams through mappings someone made on purpose:
--
--   * datasource_account_links: a person's provider account, linked by that
--     person signing in to the provider as it. Never matched by email. One
--     provider account names at most one person per organization.
--   * datasource_group_bindings: an administrator's binding of a provider group
--     onto one of the organization's teams. An unbound group grants nothing.
--   * datasource_domains: a domain an administrator claimed, verified by a DNS
--     TXT record. Only a verified domain lets "anyone in the domain" grant the
--     boundary's readers.
--
-- All three are tenant rows under the organization's policy.
--
-- datasource_credential_budgets meters the operations one provider credential
-- spends per window, shared by every source and every replica that uses it, so
-- a sync a person started is served before background syncs. A credential is
-- not always one tenant's (an unauthenticated public read is metered for the
-- whole deployment), so the table is a platform relation reached only by the
-- control plane.

CREATE TABLE public.datasource_account_links (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    org_id uuid NOT NULL,
    user_id uuid NOT NULL,
    connector text NOT NULL,
    provider_account_id text NOT NULL,
    provider_account_login text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT datasource_account_links_connector_check CHECK ((connector ~ '^[a-z][a-z0-9-]{1,31}$'::text)),
    CONSTRAINT datasource_account_links_provider_account_id_check CHECK (((length(provider_account_id) >= 1) AND (length(provider_account_id) <= 255)))
);

ALTER TABLE ONLY public.datasource_account_links FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.datasource_account_links
    ADD CONSTRAINT datasource_account_links_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.datasource_account_links
    ADD CONSTRAINT datasource_account_links_account_key UNIQUE (org_id, connector, provider_account_id);

ALTER TABLE ONLY public.datasource_account_links
    ADD CONSTRAINT datasource_account_links_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.datasource_account_links
    ADD CONSTRAINT datasource_account_links_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(uuid) ON DELETE CASCADE;

CREATE INDEX idx_datasource_account_links_user ON public.datasource_account_links USING btree (org_id, user_id);

ALTER TABLE public.datasource_account_links ENABLE ROW LEVEL SECURITY;

CREATE POLICY datasource_account_links_tenant ON public.datasource_account_links
    USING ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid));

CREATE POLICY app_control_plane_explicit_rows ON public.datasource_account_links
    TO app_control_plane
    USING ((CURRENT_USER = 'app_control_plane'::name))
    WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.datasource_account_links TO app_tenant;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.datasource_account_links TO app_control_plane;

CREATE TABLE public.datasource_group_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    org_id uuid NOT NULL,
    connector text NOT NULL,
    provider_group_id text NOT NULL,
    team_id uuid NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT datasource_group_bindings_connector_check CHECK ((connector ~ '^[a-z][a-z0-9-]{1,31}$'::text)),
    CONSTRAINT datasource_group_bindings_provider_group_id_check CHECK (((length(provider_group_id) >= 1) AND (length(provider_group_id) <= 255)))
);

ALTER TABLE ONLY public.datasource_group_bindings FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.datasource_group_bindings
    ADD CONSTRAINT datasource_group_bindings_pkey PRIMARY KEY (id);

-- A provider group is bound to one team at most: a group that meant two teams
-- would widen every item it reads to both.
ALTER TABLE ONLY public.datasource_group_bindings
    ADD CONSTRAINT datasource_group_bindings_group_key UNIQUE (org_id, connector, provider_group_id);

ALTER TABLE ONLY public.datasource_group_bindings
    ADD CONSTRAINT datasource_group_bindings_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.datasource_group_bindings
    ADD CONSTRAINT datasource_group_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.datasource_group_bindings
    ADD CONSTRAINT datasource_group_bindings_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(uuid) ON DELETE SET NULL;

CREATE INDEX idx_datasource_group_bindings_team ON public.datasource_group_bindings USING btree (team_id);

ALTER TABLE public.datasource_group_bindings ENABLE ROW LEVEL SECURITY;

CREATE POLICY datasource_group_bindings_tenant ON public.datasource_group_bindings
    USING ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid));

CREATE POLICY app_control_plane_explicit_rows ON public.datasource_group_bindings
    TO app_control_plane
    USING ((CURRENT_USER = 'app_control_plane'::name))
    WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));

GRANT SELECT,INSERT,DELETE ON TABLE public.datasource_group_bindings TO app_tenant;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.datasource_group_bindings TO app_control_plane;

CREATE TABLE public.datasource_domains (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    org_id uuid NOT NULL,
    domain text NOT NULL,
    -- The value the domain's TXT record must carry. It proves only that whoever
    -- controls the domain's DNS published it, so it is not a secret.
    verification_token text NOT NULL,
    verified_at timestamp with time zone,
    created_by uuid,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT datasource_domains_domain_check CHECK ((domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$'::text)),
    CONSTRAINT datasource_domains_verification_token_check CHECK ((length(verification_token) = 43))
);

ALTER TABLE ONLY public.datasource_domains FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.datasource_domains
    ADD CONSTRAINT datasource_domains_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.datasource_domains
    ADD CONSTRAINT datasource_domains_org_domain_key UNIQUE (org_id, domain);

ALTER TABLE ONLY public.datasource_domains
    ADD CONSTRAINT datasource_domains_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.datasource_domains
    ADD CONSTRAINT datasource_domains_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(uuid) ON DELETE SET NULL;

ALTER TABLE public.datasource_domains ENABLE ROW LEVEL SECURITY;

CREATE POLICY datasource_domains_tenant ON public.datasource_domains
    USING ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid));

CREATE POLICY app_control_plane_explicit_rows ON public.datasource_domains
    TO app_control_plane
    USING ((CURRENT_USER = 'app_control_plane'::name))
    WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.datasource_domains TO app_tenant;
GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.datasource_domains TO app_control_plane;

CREATE TABLE public.datasource_credential_budgets (
    credential_key text NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    used integer DEFAULT 0 NOT NULL,
    -- Set when the provider itself rate limited the credential: nothing spends
    -- it before then, whatever the window says.
    blocked_until timestamp with time zone,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT datasource_credential_budgets_credential_key_check CHECK (((length(credential_key) >= 1) AND (length(credential_key) <= 200))),
    CONSTRAINT datasource_credential_budgets_used_check CHECK ((used >= 0))
);

ALTER TABLE ONLY public.datasource_credential_budgets
    ADD CONSTRAINT datasource_credential_budgets_pkey PRIMARY KEY (credential_key);

GRANT SELECT,INSERT,DELETE,UPDATE ON TABLE public.datasource_credential_budgets TO app_control_plane;
