"use client";

import { useState } from "react";
import { useRoles, useCreateRole, useDeleteRole, useAssignRole, useRevokeRole } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { formatDate, truncateUUID } from "@/lib/admin-core";

type RoleRow = Record<string, unknown>;

const columns: Column<RoleRow>[] = [
  { key: "name", label: "Name" },
  { key: "description", label: "Description" },
  {
    key: "permissions",
    label: "Permissions",
    render: (v) => {
      const perms = v as { resource: string; action: string }[] | undefined;
      if (!perms || perms.length === 0)
        return <span className="text-gray-400">None</span>;
      return (
        <div className="flex flex-wrap gap-1">
          {perms.map((p, i) => (
            <span
              key={i}
              className="inline-block px-2 py-0.5 bg-gray-100 dark:bg-gray-800 rounded text-xs font-mono"
            >
              {p.resource}:{p.action}
            </span>
          ))}
        </div>
      );
    },
  },
  {
    key: "id",
    label: "ID",
    render: (v) => (
      <span className="font-mono text-xs text-gray-500">
        {truncateUUID(v as string)}
      </span>
    ),
  },
  {
    key: "createdAt",
    label: "Created",
    render: (v) => (
      <span className="text-gray-500">{formatDate(v as string)}</span>
    ),
  },
];

export default function RolesPage() {
  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [permResource, setPermResource] = useState("");
  const [permAction, setPermAction] = useState("");
  const [permissions, setPermissions] = useState<
    { resource: string; action: string }[]
  >([]);
  const [selectedRoleId, setSelectedRoleId] = useState<string | null>(null);
  const [assignUserId, setAssignUserId] = useState("");
  const [assignOrgId, setAssignOrgId] = useState("");
  const [revokeUserId, setRevokeUserId] = useState("");
  const [revokeOrgId, setRevokeOrgId] = useState("");

  const { data: roles = [], isLoading } = useRoles();
  const createRole = useCreateRole();
  const deleteRole = useDeleteRole();
  const assignRole = useAssignRole();
  const revokeRole = useRevokeRole();

  const addPermission = () => {
    if (!permResource.trim() || !permAction.trim()) return;
    setPermissions((prev) => [
      ...prev,
      { resource: permResource.trim(), action: permAction.trim() },
    ]);
    setPermResource("");
    setPermAction("");
  };

  const handleCreate = () => {
    if (!name.trim()) return;
    createRole.mutate(
      { name: name.trim(), description: description.trim(), permissions },
      {
        onSuccess: () => {
          setName("");
          setDescription("");
          setPermissions([]);
          setShowForm(false);
        },
      }
    );
  };

  const handleAssignRole = () => {
    if (!selectedRoleId || !assignUserId.trim()) return;
    assignRole.mutate(
      { subjectId: assignUserId.trim(), roleId: selectedRoleId, orgId: assignOrgId.trim() || undefined },
      { onSuccess: () => { setAssignUserId(""); setAssignOrgId(""); } },
    );
  };

  const handleRevokeRole = () => {
    if (!selectedRoleId || !revokeUserId.trim()) return;
    revokeRole.mutate(
      { subjectId: revokeUserId.trim(), roleId: selectedRoleId, orgId: revokeOrgId.trim() || undefined },
      { onSuccess: () => { setRevokeUserId(""); setRevokeOrgId(""); } },
    );
  };

  const actionsColumn: Column<RoleRow> = {
    key: "id",
    label: "Actions",
    render: (_, row) => (
      <div className="space-x-2">
        <button
          onClick={(e) => {
            e.stopPropagation();
            setSelectedRoleId(row.id as string);
          }}
          className="text-blue-600 hover:text-blue-800 text-sm font-medium"
        >
          Assignments
        </button>
        <button
          onClick={(e) => {
            e.stopPropagation();
            deleteRole.mutate(row.id as string);
          }}
          className="text-red-600 hover:text-red-800 text-sm font-medium"
        >
          Delete
        </button>
      </div>
    ),
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Roles</h2>
        <button
          onClick={() => setShowForm(!showForm)}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700"
        >
          {showForm ? "Cancel" : "Create Role"}
        </button>
      </div>

      {showForm && (
        <div className="mb-6 p-4 border border-gray-200 dark:border-gray-800 rounded-lg space-y-3">
          <input
            type="text"
            placeholder="Role name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          <input
            type="text"
            placeholder="Description (optional)"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            className="w-full px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />

          <div className="space-y-2">
            <p className="text-sm font-medium text-gray-700 dark:text-gray-300">
              Permissions
            </p>
            {permissions.map((p, i) => (
              <div key={i} className="flex items-center gap-2">
                <span className="text-xs font-mono px-2 py-1 bg-gray-100 dark:bg-gray-800 rounded">
                  {p.resource}:{p.action}
                </span>
                <button
                  onClick={() =>
                    setPermissions((prev) => prev.filter((_, idx) => idx !== i))
                  }
                  className="text-red-500 text-xs hover:text-red-700"
                >
                  Remove
                </button>
              </div>
            ))}
            <div className="flex items-center gap-2">
              <input
                type="text"
                placeholder="Resource"
                value={permResource}
                onChange={(e) => setPermResource(e.target.value)}
                className="px-3 py-1.5 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
              <input
                type="text"
                placeholder="Action"
                value={permAction}
                onChange={(e) => setPermAction(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && addPermission()}
                className="px-3 py-1.5 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
              <button
                onClick={addPermission}
                disabled={!permResource.trim() || !permAction.trim()}
                className="px-3 py-1.5 bg-gray-200 dark:bg-gray-700 rounded text-sm hover:bg-gray-300 dark:hover:bg-gray-600 disabled:opacity-50"
              >
                Add
              </button>
            </div>
          </div>

          <button
            onClick={handleCreate}
            disabled={createRole.isPending || !name.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
          >
            {createRole.isPending ? "Creating..." : "Create Role"}
          </button>
        </div>
      )}

      <DataTable
        columns={[...columns, actionsColumn]}
        data={roles as RoleRow[]}
        isLoading={isLoading}
        emptyMessage="No roles defined"
        getRowKey={(row) => row.id as string}
      />

      {selectedRoleId && (
        <div className="mt-8">
          <div className="flex items-center justify-between mb-4">
            <h3 className="text-lg font-semibold">
              Role Assignments{" "}
              <span className="text-gray-500 text-sm font-normal">
                ({roles.find((r) => (r as RoleRow).id === selectedRoleId)?.name ?? truncateUUID(selectedRoleId)})
              </span>
            </h3>
            <button
              onClick={() => setSelectedRoleId(null)}
              className="text-gray-500 hover:text-gray-700 text-sm"
            >
              Close
            </button>
          </div>

          <div className="space-y-4">
            <div className="p-4 border border-gray-200 dark:border-gray-800 rounded-lg space-y-3">
              <p className="text-sm font-medium text-gray-700 dark:text-gray-300">Assign Role</p>
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  placeholder="User ID"
                  value={assignUserId}
                  onChange={(e) => setAssignUserId(e.target.value)}
                  className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <input
                  type="text"
                  placeholder="Org ID (optional)"
                  value={assignOrgId}
                  onChange={(e) => setAssignOrgId(e.target.value)}
                  className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <button
                  onClick={handleAssignRole}
                  disabled={assignRole.isPending || !assignUserId.trim()}
                  className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
                >
                  {assignRole.isPending ? "Assigning..." : "Assign"}
                </button>
              </div>
            </div>

            <div className="p-4 border border-gray-200 dark:border-gray-800 rounded-lg space-y-3">
              <p className="text-sm font-medium text-gray-700 dark:text-gray-300">Revoke Role</p>
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  placeholder="User ID"
                  value={revokeUserId}
                  onChange={(e) => setRevokeUserId(e.target.value)}
                  className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <input
                  type="text"
                  placeholder="Org ID (optional)"
                  value={revokeOrgId}
                  onChange={(e) => setRevokeOrgId(e.target.value)}
                  className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <button
                  onClick={handleRevokeRole}
                  disabled={revokeRole.isPending || !revokeUserId.trim()}
                  className="px-4 py-2 bg-red-600 text-white rounded-lg text-sm font-medium hover:bg-red-700 disabled:opacity-50"
                >
                  {revokeRole.isPending ? "Revoking..." : "Revoke"}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
