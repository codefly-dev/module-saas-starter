-- Editing a role in place — the only way to change what it permits without
-- dropping every assignment it carries — was not expressible as a tenant. Two
-- separate layers refused it, and they fail differently, which is why both are
-- here: the missing UPDATE grant on roles raises "permission denied", while the
-- missing DELETE policy on role_permissions raises nothing at all. Under forced
-- RLS a statement with no matching policy matches zero rows, so removing a
-- permission would have reported success and changed nothing. role_permissions
-- carries no tenant column of its own (it is keyed by role_id), so the
-- tenant-table invariants in tools/rls-migration-gate.mjs never covered it.
--
-- The roles grant is deliberately column-scoped. A table-wide UPDATE would let a
-- tenant re-parent a role: roles_polymorphic's USING admits rows where org_id IS
-- NULL, and its WITH CHECK only constrains the RESULTING row, so
-- `UPDATE public.roles SET org_id = <my org> WHERE id = <a global role>`
-- satisfies both halves and moves a platform built-in into one organization,
-- removing it from every other one. Granting only the column the editor writes
-- makes that statement fail at the privilege layer, not at a policy that
-- happens to permit it.
GRANT UPDATE (description) ON TABLE public.roles TO app_tenant;
GRANT DELETE ON TABLE public.role_permissions TO app_tenant;

-- The predicate is the INSERT policy's WITH CHECK verbatim: a grant is
-- deletable exactly when it belongs to a role this organization owns. Global
-- roles (org_id IS NULL) stay out of reach of every tenant, which is what keeps
-- the built-in vocabulary stable. The UPDATE half needs no new policy —
-- roles_polymorphic already applies to every verb, and its WITH CHECK rejects a
-- result row that is not the current organization's.
CREATE POLICY role_permissions_delete ON public.role_permissions
    FOR DELETE
    USING (EXISTS (
        SELECT 1
        FROM public.roles
        WHERE roles.id = role_permissions.role_id
          AND roles.org_id IS NOT NULL
          AND roles.org_id::text = current_setting('app.current_org_id', true)
    ));
