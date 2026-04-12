"use client";

import { useActiveSessions } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { formatDate, truncateUUID } from "@/lib/admin-core";

type SessionRow = Record<string, unknown>;

const columns: Column<SessionRow>[] = [
  {
    key: "userId",
    label: "User",
    render: (v) => <span className="font-mono text-xs">{truncateUUID(v as string)}</span>,
  },
  { key: "ipAddress", label: "IP" },
  {
    key: "lastActiveAt",
    label: "Last Active",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
  {
    key: "expiresAt",
    label: "Expires",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
];

export default function SessionsPage() {
  const { data: sessions = [], isLoading } = useActiveSessions();

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Active Sessions</h2>
      <DataTable
        columns={columns}
        data={sessions as SessionRow[]}
        isLoading={isLoading}
        emptyMessage="No active sessions"
        getRowKey={(row) => row.id as string}
      />
    </div>
  );
}
