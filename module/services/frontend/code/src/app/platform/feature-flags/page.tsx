"use client";

import { useState } from "react";
import { useFeatureFlags, useUpsertFeatureFlag } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { StatusBadge } from "@/components/status-badge";

type FlagRow = Record<string, unknown>;

const columns: Column<FlagRow>[] = [
  { key: "name", label: "Name" },
  { key: "description", label: "Description" },
  {
    key: "enabled",
    label: "Status",
    render: (v) => (
      <StatusBadge
        label={v ? "Enabled" : "Disabled"}
        variant={v ? "success" : "default"}
      />
    ),
  },
  {
    key: "rolloutPercent",
    label: "Rollout %",
    render: (v) => (
      <span className="text-gray-500">{v != null ? `${v}%` : "100%"}</span>
    ),
  },
  {
    key: "targetOrgIds",
    label: "Target Orgs",
    render: (v) => {
      const ids = v as string[] | undefined;
      if (!ids || ids.length === 0)
        return <span className="text-gray-400">All</span>;
      return <span className="text-xs font-mono">{ids.length} orgs</span>;
    },
  },
];

export default function FeatureFlagsPage() {
  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [rolloutPercent, setRolloutPercent] = useState(100);

  const { data: flags = [], isLoading } = useFeatureFlags();
  const upsertFlag = useUpsertFeatureFlag();

  const handleCreate = () => {
    if (!name.trim()) return;
    upsertFlag.mutate(
      {
        name: name.trim(),
        description: description.trim() || undefined,
        enabled,
        rolloutPercent,
      },
      {
        onSuccess: () => {
          setName("");
          setDescription("");
          setEnabled(true);
          setRolloutPercent(100);
          setShowForm(false);
        },
      }
    );
  };

  const toggleColumn: Column<FlagRow> = {
    key: "name",
    label: "Actions",
    render: (_, row) => {
      const isEnabled = row.enabled as boolean;
      return (
        <button
          onClick={(e) => {
            e.stopPropagation();
            upsertFlag.mutate({
              name: row.name as string,
              description: (row.description as string) || undefined,
              enabled: !isEnabled,
              rolloutPercent: row.rolloutPercent as number | undefined,
              targetOrgIds: row.targetOrgIds as string[] | undefined,
            });
          }}
          className={`text-sm font-medium ${
            isEnabled
              ? "text-red-600 hover:text-red-800"
              : "text-green-600 hover:text-green-800"
          }`}
        >
          {isEnabled ? "Disable" : "Enable"}
        </button>
      );
    },
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Feature Flags</h2>
        <button
          onClick={() => setShowForm(!showForm)}
          className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700"
        >
          {showForm ? "Cancel" : "Create Flag"}
        </button>
      </div>

      {showForm && (
        <div className="mb-6 p-4 border border-gray-200 dark:border-gray-800 rounded-lg space-y-3">
          <input
            type="text"
            placeholder="Flag name (e.g. enable_new_dashboard)"
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
          <div className="flex items-center gap-4">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                className="rounded border-gray-300"
              />
              Enabled
            </label>
            <div className="flex items-center gap-2">
              <label className="text-sm text-gray-700 dark:text-gray-300">
                Rollout %
              </label>
              <input
                type="number"
                min={0}
                max={100}
                value={rolloutPercent}
                onChange={(e) => setRolloutPercent(Number(e.target.value))}
                className="w-20 px-3 py-1.5 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
            </div>
          </div>
          <button
            onClick={handleCreate}
            disabled={upsertFlag.isPending || !name.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
          >
            {upsertFlag.isPending ? "Saving..." : "Create Flag"}
          </button>
        </div>
      )}

      <DataTable
        columns={[...columns, toggleColumn]}
        data={flags as FlagRow[]}
        isLoading={isLoading}
        emptyMessage="No feature flags"
        getRowKey={(row) => row.name as string}
      />
    </div>
  );
}
