"use client";

import { useState } from "react";
import Link from "next/link";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";
import {
  Badge,
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/ui";
import { useRoles, useRoleAssignments } from "../service/queries";
import { useAssignRole, useRevokeRole } from "../service/mutations";

interface ManageMemberRolesDialogProps {
  orgId: string;
  userId: string;
  userLabel: string;
  trigger?: React.ReactElement;
}

// ManageMemberRolesDialog — the primary surface for assigning/
// revoking custom roles for a user within an org. Wraps the
// previously-orphan useAssignRole / useRevokeRole hooks.
//
// Visible roles = built-ins (org_id IS NULL) + this org's custom
// roles, courtesy of the polymorphic RLS policy on `roles` (Phase 2E).
// Assignments shown are filtered by subjectId so the picker shows
// only this user's grants in this org.
export function ManageMemberRolesDialog({
  orgId,
  userId,
  userLabel,
  trigger,
}: ManageMemberRolesDialogProps) {
  const [open, setOpen] = useState(false);
  const [picked, setPicked] = useState<string>("");

  const { data: roles = [] } = useRoles(orgId);
  const { data: assignments = [], isLoading } = useRoleAssignments(orgId, userId);

  const assignRole = useAssignRole();
  const revokeRole = useRevokeRole();

  // Roles already granted to this user — disable in the picker so
  // users can't double-assign (the backend's ON CONFLICT DO NOTHING
  // would silently swallow it; better to surface in the UI).
  const grantedIds = new Set(assignments.map((a) => a.roleId));
  const availableRoles = roles.filter((r) => !grantedIds.has(r.id));

  const roleNameById = new Map(roles.map((r) => [r.id, r.name] as const));

  function handleAssign() {
    if (!picked) return;
    assignRole.mutate(
      { subjectId: userId, roleId: picked, orgId },
      {
        onSuccess: () => {
          toast.success(`Role assigned to ${userLabel}`);
          setPicked("");
        },
        onError: (e) =>
          toast.error(`Failed to assign role: ${(e as Error).message}`),
      },
    );
  }

  function handleRevoke(roleId: string) {
    revokeRole.mutate(
      { subjectId: userId, roleId, orgId },
      {
        onSuccess: () => toast.success("Role revoked"),
        onError: (e) =>
          toast.error(`Failed to revoke role: ${(e as Error).message}`),
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={trigger ?? <Button variant="outline" size="sm" />}>
        {!trigger && "Manage roles"}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Manage roles</DialogTitle>
          <DialogDescription>
            Roles assigned to{" "}
            <span className="font-mono text-xs">{userLabel}</span> in this org.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <div className="text-sm font-medium">Current assignments</div>
            {isLoading ? (
              <div className="text-sm text-muted-foreground">Loading…</div>
            ) : assignments.length === 0 ? (
              <div className="text-sm text-muted-foreground">
                No custom roles assigned.
              </div>
            ) : (
              <div className="flex flex-wrap gap-1">
                {assignments.map((a) => (
                  <Badge
                    key={a.id}
                    variant="secondary"
                    className="font-mono text-xs gap-1"
                  >
                    {roleNameById.get(a.roleId) ?? a.roleId.slice(0, 8)}
                    <button
                      type="button"
                      onClick={() => handleRevoke(a.roleId)}
                      disabled={revokeRole.isPending}
                      className="ml-1 hover:text-destructive disabled:opacity-50"
                      aria-label="Revoke"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </Badge>
                ))}
              </div>
            )}
          </div>

          <div className="space-y-2">
            <div className="text-sm font-medium">Grant a role</div>
            <div className="flex items-center gap-2">
              <Select value={picked} onValueChange={(v) => setPicked(v ?? "")}>
                <SelectTrigger className="flex-1">
                  <SelectValue placeholder="Pick a role…" />
                </SelectTrigger>
                <SelectContent>
                  {availableRoles.length === 0 ? (
                    <div className="px-2 py-2 text-sm text-muted-foreground">
                      <div>No roles available.</div>
                      <Link
                        href="/admin/roles"
                        className="text-primary hover:underline"
                        onClick={() => setOpen(false)}
                      >
                        Create a custom role →
                      </Link>
                    </div>
                  ) : (
                    availableRoles.map((r) => (
                      <SelectItem key={r.id} value={r.id}>
                        <span className="flex items-center gap-2">
                          {r.name}
                          {r.builtIn ? (
                            <Badge variant="outline" className="text-[10px]">
                              built-in
                            </Badge>
                          ) : (
                            <Badge variant="secondary" className="text-[10px]">
                              custom
                            </Badge>
                          )}
                        </span>
                      </SelectItem>
                    ))
                  )}
                </SelectContent>
              </Select>
              <Button
                size="sm"
                onClick={handleAssign}
                disabled={!picked || assignRole.isPending}
              >
                <Plus className="mr-1 h-4 w-4" />
                {assignRole.isPending ? "Assigning…" : "Assign"}
              </Button>
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
