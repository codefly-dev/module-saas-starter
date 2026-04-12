"use client";

import { useState } from "react";
import { useUsers, useSuspendUser, useUnsuspendUser } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { StatusBadge } from "@/components/status-badge";
import { formatDate } from "@/lib/admin-core";
import { formatUserStatus } from "@/lib/transforms/domain";

type UserRow = Record<string, unknown>;

const columns: Column<UserRow>[] = [
  { key: "primaryEmail", label: "Email" },
  {
    key: "status",
    label: "Status",
    render: (v) => {
      const { label, variant } = formatUserStatus(v as string);
      return <StatusBadge label={label} variant={variant} />;
    },
  },
  {
    key: "createdAt",
    label: "Created",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
];

export default function UsersPage() {
  const [query, setQuery] = useState("");
  const { data: users = [], isLoading } = useUsers(query);
  const suspendUser = useSuspendUser();
  const unsuspendUser = useUnsuspendUser();

  const actionsColumn: Column<UserRow> = {
    key: "uuid",
    label: "Actions",
    render: (_, row) => {
      const status = row.status as string;
      const uuid = row.uuid as string;
      return (
        <div className="space-x-2">
          {status === "USER_STATUS_ACTIVE" && (
            <button
              onClick={(e) => { e.stopPropagation(); suspendUser.mutate({ userId: uuid, reason: "admin action" }); }}
              className="text-red-600 hover:text-red-800 text-sm font-medium"
            >
              Suspend
            </button>
          )}
          {status === "USER_STATUS_SUSPENDED" && (
            <button
              onClick={(e) => { e.stopPropagation(); unsuspendUser.mutate(uuid); }}
              className="text-green-600 hover:text-green-800 text-sm font-medium"
            >
              Unsuspend
            </button>
          )}
        </div>
      );
    },
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Users</h2>
        <input
          type="text"
          placeholder="Search by email or name..."
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg w-80 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
      </div>
      <DataTable
        columns={[...columns, actionsColumn]}
        data={users as UserRow[]}
        isLoading={isLoading}
        emptyMessage="No users found"
        getRowKey={(row) => row.uuid as string}
      />
    </div>
  );
}
