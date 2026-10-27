-- The authority core's records (codefly-dev/authority libraries/authority-core).
--
-- accounts serves the host interfaces (authority, composition, installation)
-- through the shared authority core, the same one the local authority runs on
-- an in-memory store. These are the records the core decides with and writes:
-- the modules a delivery composed and the tombstones of withdrawn ones, every
-- installation (a module's at each organization, a solution's installed by an
-- administrator), the solution roles granted on them, persons' delegations to
-- modules, the epochs and revisions that end what was issued before them, and
-- the single-use capabilities already consumed. Identity (organizations,
-- members, teams, roles) and the organization's authorization revision stay
-- accounts' own relations; the core only reads them.
--
-- Control-plane only, like every platform relation: the core decides across
-- organizations (a module delivered is installed at every one), so request
-- traffic has no access and nothing here carries a request policy. Isolation
-- between organizations is the core's decision, proven by the host-interface
-- conformance suite.

CREATE TABLE public.authority_core_modules (
    name text NOT NULL,
    -- The audience every capability to the module names; a module keeps it for life.
    principal text NOT NULL,
    kind text NOT NULL,
    generation bigint NOT NULL,
    -- A fixture's stand-in until the module's own delivery replaces it.
    stand_in boolean DEFAULT false NOT NULL,
    -- What it serves, its bindings, a solution's roles, its method policies.
    declarations jsonb NOT NULL,
    CONSTRAINT authority_core_modules_pkey PRIMARY KEY (name),
    CONSTRAINT authority_core_modules_principal_key UNIQUE (principal),
    CONSTRAINT authority_core_modules_kind_check CHECK ((kind = ANY (ARRAY['module'::text, 'solution'::text]))),
    CONSTRAINT authority_core_modules_generation_check CHECK ((generation > 0))
);

-- What stays of a withdrawn module, never removed: its principal is its own
-- for life and a redelivery must exceed its generation.
CREATE TABLE public.authority_core_tombstones (
    name text NOT NULL,
    principal text NOT NULL,
    generation bigint NOT NULL,
    CONSTRAINT authority_core_tombstones_pkey PRIMARY KEY (name),
    CONSTRAINT authority_core_tombstones_principal_key UNIQUE (principal)
);

CREATE TABLE public.authority_core_installations (
    id text NOT NULL,
    organization_id uuid NOT NULL,
    target text NOT NULL,
    status text NOT NULL,
    revision bigint NOT NULL,
    -- A solution installation's own role, its principal and its installer;
    -- empty for a module's.
    agent_role text DEFAULT ''::text NOT NULL,
    principal_id text DEFAULT ''::text NOT NULL,
    installed_by text DEFAULT ''::text NOT NULL,
    -- The last reviewed consent operation.
    consent jsonb DEFAULT '{}'::jsonb NOT NULL,
    -- The resource ids attached to the installation, by kind.
    owned jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT authority_core_installations_pkey PRIMARY KEY (id),
    CONSTRAINT authority_core_installations_organization_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE,
    CONSTRAINT authority_core_installations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'revoked'::text])))
);

CREATE INDEX authority_core_installations_organization_idx ON public.authority_core_installations USING btree (organization_id);
CREATE UNIQUE INDEX authority_core_installations_principal_key ON public.authority_core_installations USING btree (principal_id) WHERE (principal_id <> ''::text);

-- A module's installation is not here: it is accounts' own record
-- (public.installations, kind 'module'), an administrator's consent within the
-- module envelope, retired by an uninstall or by a ceiling that no longer
-- allows the organization. The core reads those records as its installations
-- (it is configured so that it installs no module itself) and only ever raises
-- one's revision, when a redelivery narrows the module. accounts' own revision
-- is lifecycle-owned, so the raise is kept here, beside the record, and the
-- revision the core reads is the sum.
CREATE TABLE public.authority_core_module_revisions (
    installation_id uuid NOT NULL,
    raised bigint NOT NULL,
    CONSTRAINT authority_core_module_revisions_pkey PRIMARY KEY (installation_id),
    CONSTRAINT authority_core_module_revisions_installation_fkey FOREIGN KEY (installation_id) REFERENCES public.installations(id) ON DELETE CASCADE,
    CONSTRAINT authority_core_module_revisions_raised_check CHECK ((raised > 0))
);

-- One role on one installation, held by exactly one person or team.
CREATE TABLE public.authority_core_role_grants (
    installation_id text NOT NULL,
    role text NOT NULL,
    person_id text DEFAULT ''::text NOT NULL,
    team_id text DEFAULT ''::text NOT NULL,
    CONSTRAINT authority_core_role_grants_pkey PRIMARY KEY (installation_id, role, person_id, team_id),
    CONSTRAINT authority_core_role_grants_subject_check CHECK (((person_id = ''::text) <> (team_id = ''::text)))
);

CREATE TABLE public.authority_core_delegations (
    id text NOT NULL,
    organization_id uuid NOT NULL,
    grantor text NOT NULL,
    module text NOT NULL,
    purpose text DEFAULT ''::text NOT NULL,
    scopes jsonb NOT NULL,
    revoked boolean DEFAULT false NOT NULL,
    CONSTRAINT authority_core_delegations_pkey PRIMARY KEY (id),
    CONSTRAINT authority_core_delegations_organization_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON DELETE CASCADE
);

-- Counters that only grow: a person the host reported changed, a person's
-- role revoked on one installation, an audience that withdrew owner-aware
-- checking. Each ends the capabilities sealed before it moved.
CREATE TABLE public.authority_core_person_epochs (
    person_id text NOT NULL,
    epoch bigint NOT NULL,
    CONSTRAINT authority_core_person_epochs_pkey PRIMARY KEY (person_id),
    CONSTRAINT authority_core_person_epochs_epoch_check CHECK ((epoch > 0))
);

CREATE TABLE public.authority_core_grant_epochs (
    person_id text NOT NULL,
    installation_id text NOT NULL,
    epoch bigint NOT NULL,
    CONSTRAINT authority_core_grant_epochs_pkey PRIMARY KEY (person_id, installation_id),
    CONSTRAINT authority_core_grant_epochs_epoch_check CHECK ((epoch > 0))
);

CREATE TABLE public.authority_core_owner_aware (
    audience text NOT NULL,
    revision bigint NOT NULL,
    CONSTRAINT authority_core_owner_aware_pkey PRIMARY KEY (audience),
    CONSTRAINT authority_core_owner_aware_revision_check CHECK ((revision > 0))
);

-- A single-use capability's nonce, consumed once across every replica and
-- kept until the capability expires.
CREATE TABLE public.authority_core_replay (
    nonce text NOT NULL,
    retain_until timestamp with time zone NOT NULL,
    CONSTRAINT authority_core_replay_pkey PRIMARY KEY (nonce)
);

CREATE INDEX authority_core_replay_retain_until_idx ON public.authority_core_replay USING btree (retain_until);

-- The delegations accounts recorded itself before the core decided them
-- (migration 51) are the core's now, as they stood: active or revoked. The
-- capabilities issued under them were sealed to a binding id the core does
-- not issue, so they end here and are issued again from the same delegation.
INSERT INTO public.authority_core_delegations (id, organization_id, grantor, module, purpose, scopes, revoked)
SELECT d.id::text, d.org_id, d.principal_id::text, d.module_prefix, d.purpose,
       COALESCE((SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
                    'kind', s->>'resource_kind', 'actions', s->'actions', 'ids', s->'resource_ids')))
                   FROM jsonb_array_elements(d.scopes) AS s), '[]'::jsonb),
       d.revoked_at IS NOT NULL
  FROM public.module_delegations d;

DROP TABLE public.module_delegations;
DROP FUNCTION public.module_delegation_immutable();

GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE public.authority_core_modules TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_tombstones TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_installations TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_module_revisions TO app_control_plane;
GRANT SELECT,INSERT,DELETE ON TABLE public.authority_core_role_grants TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_delegations TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_person_epochs TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_grant_epochs TO app_control_plane;
GRANT SELECT,INSERT,UPDATE ON TABLE public.authority_core_owner_aware TO app_control_plane;
GRANT SELECT,INSERT,DELETE ON TABLE public.authority_core_replay TO app_control_plane;
