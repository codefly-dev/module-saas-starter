-- Connect-time source delegations.
--
-- When a person connects a datasource, or reconnects it, the host records that
-- they delegated the source's sync to one installed operation binding of a
-- consuming module: the module principal (by its registration prefix) and the
-- key of its `operation_audiences` entry whose `source_delegation_scopes` the
-- delegation may confer. The module later mints a short-lived operation context
-- from it, owned by that person, in the source's organization.
--
-- A row is never deleted by the host: revocation stamps revoked_at and a reason,
-- so an administrator can still see what was delegated, by whom, and why it
-- stopped. source_id and principal_id therefore carry no foreign key — the
-- record outlives the source it named and the person who made it. The mint
-- re-checks both, and every other fact, on every issuance.
--
-- binding_digest is the SHA-256 (hex) of the binding's audience and delegation
-- scopes when the person connected, so a binding that changed since is refused
-- rather than silently conferring authority nobody delegated.

CREATE TABLE public.source_delegations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    org_id uuid NOT NULL,
    source_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    module_prefix text NOT NULL,
    binding_id text NOT NULL,
    binding_digest text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    revoked_at timestamp with time zone,
    revoked_reason text,
    revoked_by uuid,
    CONSTRAINT source_delegations_module_prefix_check CHECK ((module_prefix ~ '^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$'::text) AND (length(module_prefix) <= 63)),
    CONSTRAINT source_delegations_binding_id_check CHECK (((length(binding_id) >= 1) AND (length(binding_id) <= 128))),
    CONSTRAINT source_delegations_binding_digest_check CHECK ((binding_digest ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT source_delegations_revoked_reason_check CHECK ((revoked_reason = ANY (ARRAY['revoked'::text, 'replaced'::text, 'source_deleted'::text, 'member_removed'::text, 'permission_lost'::text, 'user_inactive'::text, 'binding_changed'::text]))),
    -- A revocation is whole or absent: a stamp without a reason (or the reverse)
    -- is neither active nor explained. The comparison of two IS NULL tests is
    -- never NULL, so this cannot pass vacuously.
    CONSTRAINT source_delegations_revocation_whole CHECK (((revoked_at IS NULL) = (revoked_reason IS NULL)))
);

ALTER TABLE ONLY public.source_delegations FORCE ROW LEVEL SECURITY;

ALTER TABLE ONLY public.source_delegations
    ADD CONSTRAINT source_delegations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.source_delegations
    ADD CONSTRAINT source_delegations_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(id) ON DELETE CASCADE;

-- One active delegation per source and module: a reconnect revokes the previous
-- one in the same transaction that records its replacement.
CREATE UNIQUE INDEX source_delegations_active_source_module ON public.source_delegations USING btree (source_id, module_prefix) WHERE (revoked_at IS NULL);

CREATE INDEX source_delegations_active_principal ON public.source_delegations USING btree (org_id, principal_id, module_prefix) WHERE (revoked_at IS NULL);

CREATE INDEX source_delegations_org_created ON public.source_delegations USING btree (org_id, created_at DESC);

ALTER TABLE public.source_delegations ENABLE ROW LEVEL SECURITY;

CREATE POLICY source_delegations_tenant ON public.source_delegations
    USING ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid))
    WITH CHECK ((org_id = (NULLIF(current_setting('app.current_org_id'::text, true), ''::text))::uuid));

CREATE POLICY app_control_plane_explicit_rows ON public.source_delegations
    TO app_control_plane
    USING ((CURRENT_USER = 'app_control_plane'::name))
    WITH CHECK ((CURRENT_USER = 'app_control_plane'::name));

-- No DELETE for either role: a delegation is revoked, never erased (the
-- organization's own deletion removes its rows through the foreign key).
GRANT SELECT,INSERT,UPDATE ON TABLE public.source_delegations TO app_tenant;
GRANT SELECT,INSERT,UPDATE ON TABLE public.source_delegations TO app_control_plane;
