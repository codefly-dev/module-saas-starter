"use client";

import { useState } from "react";
import { useOrgEntitlements } from "@/lib/hooks";
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
  const { data, isLoading } = useOrgEntitlements(orgId || null);

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-4">
          <h2 className="text-2xl font-bold">Entitlements</h2>
          {data?.planName && <StatusBadge label={data.planName} variant="default" />}
        </div>
        <OrgSelector value={orgId} onChange={setOrgId} />
      </div>
      <DataTable
        columns={columns}
        data={(data?.entitlements ?? []) as EntRow[]}
        isLoading={orgId ? isLoading : false}
        emptyMessage={orgId ? "No entitlements" : "Select an organization"}
        getRowKey={(row) => row.feature as string}
      />
    </div>
  );
}
