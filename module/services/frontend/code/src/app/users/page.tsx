"use client";

import { useState } from "react";
import { useUsers, useSuspendUser, useUnsuspendUser, useImpersonateUser } from "@/lib/hooks";
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
  const [impersonationToken, setImpersonationToken] = useState<string | null>(null);
  const { data: users = [], isLoading } = useUsers(query);
  const suspendUser = useSuspendUser();
  const unsuspendUser = useUnsuspendUser();
  const impersonateUser = useImpersonateUser();

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
          <button
            onClick={(e) => {
              e.stopPropagation();
              impersonateUser.mutate(uuid, {
                onSuccess: (data) => {
                  const token = (data as Record<string, unknown>).accessToken as string | undefined;
                  if (token) setImpersonationToken(token);
                },
              });
            }}
            disabled={impersonateUser.isPending}
            className="text-purple-600 hover:text-purple-800 text-sm font-medium"
          >
            Impersonate
          </button>
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

      {impersonationToken && (
        <div className="mb-6 p-4 border border-purple-300 dark:border-purple-700 bg-purple-50 dark:bg-purple-900/20 rounded-lg">
          <div className="flex items-center justify-between mb-2">
            <span className="text-sm font-medium text-purple-800 dark:text-purple-300">
              Impersonation Token
            </span>
            <button
              onClick={() => setImpersonationToken(null)}
              className="text-purple-600 hover:text-purple-800 text-sm"
            >
              Dismiss
            </button>
          </div>
          <code className="block text-xs font-mono break-all text-purple-700 dark:text-purple-400 bg-purple-100 dark:bg-purple-900/40 p-2 rounded">
            {impersonationToken}
          </code>
        </div>
      )}

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
