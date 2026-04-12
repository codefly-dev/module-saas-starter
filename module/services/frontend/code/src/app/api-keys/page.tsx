"use client";

import { useState } from "react";
import { useAPIKeys, useRevokeAPIKey, useCreateAPIKey } from "@/lib/hooks";
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
  const [showForm, setShowForm] = useState(false);
  const [keyName, setKeyName] = useState("");
  const [environment, setEnvironment] = useState(1);
  const { data: keys = [], isLoading } = useAPIKeys(orgId || null);
  const revokeKey = useRevokeAPIKey();
  const createKey = useCreateAPIKey();

  const handleCreate = () => {
    if (!orgId || !keyName.trim()) return;
    createKey.mutate(
      { organizationId: orgId, name: keyName.trim(), environment },
      {
        onSuccess: () => {
          setKeyName("");
          setEnvironment(1);
          setShowForm(false);
        },
      }
    );
  };

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
        <div className="flex items-center gap-3">
          {orgId && (
            <button
              onClick={() => setShowForm(!showForm)}
              className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700"
            >
              {showForm ? "Cancel" : "Create Key"}
            </button>
          )}
          <OrgSelector value={orgId} onChange={setOrgId} />
        </div>
      </div>
      {showForm && orgId && (
        <div className="mb-6 p-4 border border-gray-200 dark:border-gray-800 rounded-lg flex items-end gap-3">
          <div className="flex-1">
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Key Name
            </label>
            <input
              type="text"
              placeholder="my-api-key"
              value={keyName}
              onChange={(e) => setKeyName(e.target.value)}
              className="w-full px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Environment
            </label>
            <select
              value={environment}
              onChange={(e) => setEnvironment(Number(e.target.value))}
              className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              <option value={1}>Test</option>
              <option value={2}>Live</option>
            </select>
          </div>
          <button
            onClick={handleCreate}
            disabled={createKey.isPending || !keyName.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
          >
            {createKey.isPending ? "Creating..." : "Create"}
          </button>
        </div>
      )}
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
