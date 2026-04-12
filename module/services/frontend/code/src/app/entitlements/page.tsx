"use client";

import { useState } from "react";
import { useOrgEntitlements, useOverrideEntitlement } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { StatusBadge } from "@/components/status-badge";
import { OrgSelector } from "@/components/org-selector";
import { formatLimit } from "@/lib/admin-core";

type EntRow = Record<string, unknown>;

function UsageBar({ limit, used }: { limit: number; used: number }) {
  if (limit <= 0) return null;
  const pct = Math.min(100, Math.round((used / limit) * 100));
  const color = pct > 80 ? "bg-red-500" : pct > 50 ? "bg-yellow-500" : "bg-green-500";
  return (
    <div className="flex items-center gap-2">
      <div className="flex-1 bg-gray-200 dark:bg-gray-700 rounded-full h-2">
        <div className={`h-2 rounded-full ${color}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="text-xs text-gray-500">{pct}%</span>
    </div>
  );
}

const columns: Column<EntRow>[] = [
  { key: "feature", label: "Feature" },
  { key: "limit", label: "Limit", render: (v) => formatLimit(Number(v ?? 0)) },
  { key: "used", label: "Used", render: (v) => Number(v ?? 0).toLocaleString() },
  {
    key: "limit",
    label: "Usage",
    render: (_, row) => <UsageBar limit={Number(row.limit ?? 0)} used={Number(row.used ?? 0)} />,
    className: "w-48",
  },
  {
    key: "hasOverride",
    label: "Override",
    render: (v) => (v ? <StatusBadge label="Override" variant="default" /> : null),
  },
];

export default function EntitlementsPage() {
  const [orgId, setOrgId] = useState("");
  const [overrideFeature, setOverrideFeature] = useState<string | null>(null);
  const [overrideLimitValue, setOverrideLimitValue] = useState("");
  const [overrideReason, setOverrideReason] = useState("");

  const { data, isLoading } = useOrgEntitlements(orgId || null);
  const overrideEntitlement = useOverrideEntitlement();

  const handleOverride = () => {
    if (!orgId || !overrideFeature || !overrideLimitValue) return;
    overrideEntitlement.mutate(
      {
        orgId,
        feature: overrideFeature,
        limitValue: BigInt(overrideLimitValue),
        reason: overrideReason.trim() || undefined,
      },
      {
        onSuccess: () => {
          setOverrideFeature(null);
          setOverrideLimitValue("");
          setOverrideReason("");
        },
      },
    );
  };

  const overrideColumn: Column<EntRow> = {
    key: "feature",
    label: "Actions",
    render: (_, row) => (
      <button
        onClick={(e) => {
          e.stopPropagation();
          setOverrideFeature(row.feature as string);
          setOverrideLimitValue(String(row.limit ?? 0));
        }}
        className="text-blue-600 hover:text-blue-800 text-sm font-medium"
      >
        Override
      </button>
    ),
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-4">
          <h2 className="text-2xl font-bold">Entitlements</h2>
          {data?.planName && <StatusBadge label={data.planName} variant="default" />}
        </div>
        <OrgSelector value={orgId} onChange={setOrgId} />
      </div>

      {overrideFeature && (
        <div className="mb-6 p-4 border border-gray-200 dark:border-gray-800 rounded-lg space-y-3">
          <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300">
            Override Entitlement: <span className="font-mono">{overrideFeature}</span>
          </h3>
          <div className="flex items-center gap-2">
            <input
              type="number"
              placeholder="Limit value"
              value={overrideLimitValue}
              onChange={(e) => setOverrideLimitValue(e.target.value)}
              className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 w-40"
            />
            <input
              type="text"
              placeholder="Reason (optional)"
              value={overrideReason}
              onChange={(e) => setOverrideReason(e.target.value)}
              className="flex-1 px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
            <button
              onClick={handleOverride}
              disabled={overrideEntitlement.isPending || !overrideLimitValue}
              className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
            >
              {overrideEntitlement.isPending ? "Saving..." : "Apply Override"}
            </button>
            <button
              onClick={() => { setOverrideFeature(null); setOverrideLimitValue(""); setOverrideReason(""); }}
              className="px-4 py-2 text-gray-600 hover:text-gray-800 text-sm"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      <DataTable
        columns={[...columns, overrideColumn]}
        data={(data?.entitlements ?? []) as EntRow[]}
        isLoading={orgId ? isLoading : false}
        emptyMessage={orgId ? "No entitlements" : "Select an organization"}
        getRowKey={(row) => row.feature as string}
      />
    </div>
  );
}
