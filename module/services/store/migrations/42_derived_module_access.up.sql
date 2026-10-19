-- A solution's grant CARRIES the module access the solution declares it
-- consumes (codefly-dev/module-saas-starter#1167). Two columns make that
-- expressible without a second authorization record shape.
--
-- `roles.derived_from` marks a role this host derived and owns: its permissions
-- are the solution's declared consumption for ONE reached module, intersected
-- with the role an administrator chose. It is not a role anyone may pick, edit,
-- delete, assign organisation-wide or hand-grant — a derived role that could be
-- granted by hand would widen a person's own reach into that module to whatever
-- it permits, which is the property this design exists to deny. Every guard
-- that enforces that reads this column, so NULL stays the ordinary case and no
-- existing role changes meaning.
--
-- The value is the provenance itself, not a flag: the installation it was
-- derived from, the module audience it is for, and the chosen role it was
-- intersected with. It is therefore globally unique, which is what makes
-- re-deriving it an upsert rather than a duplicate.
ALTER TABLE public.roles
    ADD COLUMN derived_from text
        CONSTRAINT roles_derived_from_check CHECK (derived_from IS NULL OR derived_from <> ''::text);

CREATE UNIQUE INDEX roles_derived_from_unique ON public.roles (derived_from) WHERE derived_from IS NOT NULL;

-- `scope_grants.derived_from_grant_id` names the grant that CAUSED a derived
-- grant: the exposure grant of the solution at the solution installation's
-- authority root. A derived grant is an ordinary scope grant at the reached
-- module's own authority-root node — the only thing that makes a module
-- reachable, resolved by the same ListAccessibleScopes union as any other grant,
-- so there is no second answer to "what does this viewer reach" — and this is
-- the one fact that distinguishes it: it has a cause, and it is revoked with it.
--
-- ON DELETE CASCADE is the structural half of that. RevokeInstallationFromTeam
-- and UninstallSolution revoke the derived rows explicitly, each through the
-- same policy-logged narrowing and with the same scope.revoked event and audit
-- record as any other revocation, so a person reading the record sees why it
-- went. The cascade is what closes the paths that do not go through them — a
-- bare RevokeScope of the cause, an organisation deletion — so a derived grant
-- can never outlive its cause even when nothing thought to remove it.
ALTER TABLE public.scope_grants
    ADD COLUMN derived_from_grant_id uuid
        REFERENCES public.scope_grants(id) ON DELETE CASCADE
        CONSTRAINT scope_grants_derived_from_not_self CHECK (derived_from_grant_id IS NULL OR derived_from_grant_id <> id);

CREATE INDEX scope_grants_derived_from_grant_id ON public.scope_grants (derived_from_grant_id)
    WHERE derived_from_grant_id IS NOT NULL;

