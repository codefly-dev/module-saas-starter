import { useQuery } from "@tanstack/react-query";
import { usePermissionService } from "@/lib/hooks/use-api-client";
import { SubjectKind } from "@/gen/saas-starter_api_grpc_pb";

// useRoles — global view (built-in roles only) when orgId omitted,
// or org-scoped (built-ins + that org's custom roles) when provided.
// The backend's polymorphic policy on `roles` (RLS Phase 2E) makes
// built-ins globally readable; custom roles are tenant-scoped.
export function useRoles(orgId?: string) {
  const svc = usePermissionService();
  return useQuery({
    queryKey: ["roles", orgId ?? ""],
    queryFn: () => svc.listRoles({ orgId: orgId ?? "" }),
    select: (data) => data.roles,
  });
}

// useRoleAssignments — per-org role assignments. Filters to one
// subject (user OR team) when subjectId + subjectKind are passed.
// The OrgMembersPanel passes SubjectKind.USER; a future team panel
// would pass SubjectKind.TEAM. Defaults to USER (matching the only
// caller today) — passing UNSPECIFIED would surface BOTH user and
// team grants whose id happens to match subjectId, which is a
// cross-kind leakage risk if UUIDs ever collide.
export function useRoleAssignments(
  orgId: string,
  subjectId?: string,
  subjectKind: SubjectKind = SubjectKind.USER,
) {
  const svc = usePermissionService();
  return useQuery({
    queryKey: ["role-assignments", orgId, subjectId ?? "", subjectKind],
    enabled: !!orgId,
    queryFn: () =>
      svc.listRoleAssignments({
        orgId,
        subjectId: subjectId ?? "",
        subjectKind,
      }),
    select: (data) => data.assignments,
  });
}
