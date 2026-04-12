"use client";

import { useRoles } from "../service/queries";
import { RolesTable } from "./roles-table";
import { RoleForm } from "./role-form";
import type { Role } from "../model/types";

export function RolesPage() {
  const { data: roles = [], isLoading } = useRoles();

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Roles</h1>
          <p className="text-muted-foreground">
            Manage roles and their permissions.
          </p>
        </div>
        <RoleForm />
      </div>

      <RolesTable roles={roles as Role[]} isLoading={isLoading} />
    </div>
  );
}
