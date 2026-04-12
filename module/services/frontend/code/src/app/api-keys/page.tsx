"use client";

import { useState } from "react";
import { useAPIKeys, useRevokeAPIKey } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { StatusBadge } from "@/components/status-badge";
import { OrgSelector } from "@/components/org-selector";
import { formatDate } from "@/lib/admin-core";

type KeyRow = Record<string, unknown>;

const columns: Column<KeyRow>[] = [
  { key: "name", label: "Name" },
  {
    key: "prefix",
    label: "Prefix",
    render: (v) => <span className="font-mono text-xs">{v as string}...</span>,
  },
  {
    key: "environment",
    label: "Env",
    render: (v) => {
      const env = (v as string)?.replace("API_KEY_ENVIRONMENT_", "").toLowerCase();
      return <StatusBadge label={env ?? "unknown"} variant={env === "live" ? "success" : "warning"} />;
    },
  },
  {
    key: "lastUsedAt",
    label: "Last Used",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
];

export default function APIKeysPage() {
  const [orgId, setOrgId] = useState("");
  const { data: keys = [], isLoading } = useAPIKeys(orgId || null);
  const revokeKey = useRevokeAPIKey();

  const actionsColumn: Column<KeyRow> = {
    key: "id",
    label: "Actions",
    render: (_, row) =>
      !row.revokedAt ? (
        <button
          onClick={() => revokeKey.mutate(row.id as string)}
          className="text-red-600 hover:text-red-800 text-sm font-medium"
        >
          Revoke
        </button>
      ) : (
        <span className="text-gray-400 text-xs">Revoked</span>
      ),
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">API Keys</h2>
        <OrgSelector value={orgId} onChange={setOrgId} />
      </div>
      <DataTable
        columns={[...columns, actionsColumn]}
        data={keys as KeyRow[]}
        isLoading={orgId ? isLoading : false}
        emptyMessage={orgId ? "No API keys" : "Select an organization"}
        getRowKey={(row) => row.id as string}
      />
    </div>
  );
}
