import { useMutation, useQueryClient } from "@tanstack/react-query";
import { usePermissionService } from "@/lib/hooks/use-api-client";

export function useCreateRole() {
  const svc = usePermissionService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      description,
      permissions,
    }: {
      name: string;
      description?: string;
      permissions?: { resource: string; action: string }[];
    }) =>
      svc.createRole({
        name,
        description: description ?? "",
        permissions: permissions ?? [],
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["roles"] }),
  });
}

export function useDeleteRole() {
  const svc = usePermissionService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => svc.deleteRole({ id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["roles"] }),
  });
}

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
      orgId?: string;
      scope?: string;
    }) =>
      svc.assignRole({
        subjectId,
        subjectKind: subjectKind ?? 1,
        roleId,
        orgId: orgId ?? "",
        scope: scope ?? "",
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["roles"] }),
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
    }: {
      subjectId: string;
      roleId: string;
      orgId?: string;
    }) =>
      svc.revokeRole({
        subjectId,
        roleId,
        orgId: orgId ?? "",
        scope: "",
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["roles"] }),
  });
}
