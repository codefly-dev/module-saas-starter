import { useMutation, useQueryClient } from "@tanstack/react-query";
import { usePermissionService } from "@/lib/hooks/use-api-client";
import { SubjectKind } from "@/gen/saas-starter_api_grpc_pb";

// useCreateRole — pass orgId to scope the role to a tenant. Empty
// orgId creates a global (built-in-equivalent) role and requires
// platform-admin authz at the backend (handler-enforced).
export function useCreateRole() {
  const svc = usePermissionService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      description,
      permissions,
      orgId,
    }: {
      name: string;
      description?: string;
      permissions?: { resource: string; action: string }[];
      orgId?: string;
    }) =>
      svc.createRole({
        name,
        description: description ?? "",
        permissions: permissions ?? [],
        orgId: orgId ?? "",
      }),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ["roles", vars.orgId ?? ""] });
      qc.invalidateQueries({ queryKey: ["roles", ""] });
    },
  });
}

export function useDeleteRole() {
  const svc = usePermissionService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => svc.deleteRole({ id }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["roles"] });
      // Deleting a role cascades to role_assignments via FK
      // ON DELETE CASCADE in migration 4. Any open dialog showing
      // grants for this role would otherwise display stale chips
      // until next mount.
      qc.invalidateQueries({ queryKey: ["role-assignments"] });
    },
  });
}

// useAssignRole — grant a role to a user (or team) within an org.
// Backend wraps in WithOrgTx scoped to orgId; the role_assignments
// row carries the same orgId so RLS lets it through.
export function useAssignRole() {
  const svc = usePermissionService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      subjectId,
      subjectKind,
      roleId,
      orgId,
      scope,
    }: {
      subjectId: string;
      subjectKind?: number;
      roleId: string;
      orgId: string;
      scope?: string;
    }) =>
      svc.assignRole({
        subjectId,
        subjectKind: subjectKind ?? SubjectKind.USER,
        roleId,
        orgId,
        scope: scope ?? "",
      }),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ["role-assignments", vars.orgId] });
    },
  });
}

export function useRevokeRole() {
  const svc = usePermissionService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      subjectId,
      roleId,
      orgId,
      scope,
    }: {
      subjectId: string;
      roleId: string;
      orgId: string;
      scope?: string;
    }) =>
      svc.revokeRole({
        subjectId,
        roleId,
        orgId,
        scope: scope ?? "",
      }),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ["role-assignments", vars.orgId] });
    },
  });
}
