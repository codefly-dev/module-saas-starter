"use client";

import { useOrganizations } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { formatDate } from "@/lib/admin-core";

type OrgRow = Record<string, unknown>;

const columns: Column<OrgRow>[] = [
  { key: "name", label: "Name" },
  { key: "slug", label: "Slug" },
  {
    key: "createdAt",
    label: "Created",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
];

export default function OrganizationsPage() {
  const { data: orgs = [], isLoading } = useOrganizations();

  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Organizations</h2>
      <DataTable
        columns={columns}
        data={orgs as OrgRow[]}
        isLoading={isLoading}
        emptyMessage="No organizations"
        getRowKey={(row) => row.id as string}
      />
    </div>
  );
}
