"use client";

import { useState } from "react";
import { useAuditLog } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { StatusBadge } from "@/components/status-badge";
import { formatDate, truncateUUID } from "@/lib/admin-core";

const ACTION_TYPES = [
  "user.registered", "user.suspended", "user.unsuspended",
  "auth.login", "auth.logout",
  "api_key.created", "api_key.revoked",
  "role.assigned", "role.revoked",
  "invitation.created", "invitation.accepted",
  "org.created", "org.member_added", "org.member_removed",
  "platform.role_granted", "platform.role_revoked", "platform.user_impersonated",
  "feature_flag.updated",
];

type AuditRow = Record<string, unknown>;

const columns: Column<AuditRow>[] = [
  {
    key: "createdAt",
    label: "Time",
    render: (v) => <span className="text-gray-500 whitespace-nowrap">{formatDate(v as string)}</span>,
  },
  {
    key: "action",
    label: "Action",
    render: (v) => <StatusBadge label={v as string} variant="default" />,
  },
  {
    key: "actorId",
    label: "Actor",
    render: (v) => <span className="font-mono text-xs">{truncateUUID(v as string)}</span>,
  },
  {
    key: "resource",
    label: "Resource",
    render: (_, row) => (
      <span className="text-gray-500">
        {row.resource as string}/{truncateUUID(row.resourceId as string)}
      </span>
    ),
  },
];

export default function AuditLogPage() {
  const [actionFilter, setActionFilter] = useState("");
  const { data, isLoading } = useAuditLog({
    action: actionFilter || undefined,
    page_size: 100,
  });

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Audit Log</h2>
        <select
          value={actionFilter}
          onChange={(e) => setActionFilter(e.target.value)}
          className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          <option value="">All actions</option>
          {ACTION_TYPES.map((action) => (
            <option key={action} value={action}>{action}</option>
          ))}
        </select>
      </div>
      <DataTable
        columns={columns}
        data={(data?.events ?? []) as AuditRow[]}
        isLoading={isLoading}
        emptyMessage="No events"
        getRowKey={(row) => row.id as string}
      />
    </div>
  );
}
