"use client";

import { useState } from "react";
import {
  usePlatformAdmins,
  useGrantPlatformRole,
  useRevokePlatformRole,
} from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { StatusBadge } from "@/components/status-badge";
import { truncateUUID } from "@/lib/admin-core";

type AdminRow = Record<string, unknown>;

const PLATFORM_ROLES = ["super_admin", "billing", "support"] as const;

const columns: Column<AdminRow>[] = [
  {
    key: "userId",
    label: "User ID",
    render: (v) => (
      <span className="font-mono text-xs">{truncateUUID(v as string)}</span>
    ),
  },
  { key: "email", label: "Email" },
  {
    key: "platformRole",
    label: "Role",
    render: (v) => {
      const role = v as string;
      const variant =
        role === "super_admin"
          ? "error"
          : role === "billing"
            ? "warning"
            : "default";
      return <StatusBadge label={role ?? "unknown"} variant={variant} />;
    },
  },
];

export default function PlatformAdminsPage() {
  const [showForm, setShowForm] = useState(false);
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState<string>(PLATFORM_ROLES[0]);

  const { data: admins = [], isLoading } = usePlatformAdmins();
  const grantRole = useGrantPlatformRole();
  const revokeRole = useRevokePlatformRole();

  const handleGrant = () => {
    if (!userId.trim()) return;
    grantRole.mutate(
      { userId: userId.trim(), platformRole: role },
      {
        onSuccess: () => {
          setUserId("");
          setShowForm(false);
        },
      }
    );
  };

  const actionsColumn: Column<AdminRow> = {
    key: "userId",
    label: "Actions",
    render: (_, row) => (
      <button
        onClick={(e) => {
          e.stopPropagation();
          revokeRole.mutate(row.userId as string);
        }}
        className="text-red-600 hover:text-red-800 text-sm font-medium"
      >
        Revoke
      </button>
    ),
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Platform Admins</h2>
        <button
          onClick={() => setShowForm(!showForm)}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700"
        >
          {showForm ? "Cancel" : "Grant Role"}
        </button>
      </div>

      {showForm && (
        <div className="mb-6 p-4 border border-gray-200 dark:border-gray-800 rounded-lg flex items-end gap-3">
          <div className="flex-1">
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              User ID
            </label>
            <input
              type="text"
              placeholder="User UUID"
              value={userId}
              onChange={(e) => setUserId(e.target.value)}
              className="w-full px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Role
            </label>
            <select
              value={role}
              onChange={(e) => setRole(e.target.value)}
              className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              {PLATFORM_ROLES.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
          </div>
          <button
            onClick={handleGrant}
            disabled={grantRole.isPending || !userId.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
          >
            {grantRole.isPending ? "Granting..." : "Grant"}
          </button>
        </div>
      )}

      <DataTable
        columns={[...columns, actionsColumn]}
        data={admins as AdminRow[]}
        isLoading={isLoading}
        emptyMessage="No platform admins"
        getRowKey={(row) => row.userId as string}
      />
    </div>
  );
}
