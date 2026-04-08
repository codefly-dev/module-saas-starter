"use client";

import { useState } from "react";
import { useInvitations, useRevokeInvitation } from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { StatusBadge } from "@/components/status-badge";
import { OrgSelector } from "@/components/org-selector";
import { formatDate } from "@/lib/admin-core";
import { formatInvitationStatus } from "@/lib/transforms/domain";

type InvRow = Record<string, unknown>;

const columns: Column<InvRow>[] = [
  { key: "email", label: "Email" },
  { key: "role", label: "Role" },
  {
    key: "status",
    label: "Status",
    render: (v) => {
      const raw = (v as string)?.replace("INVITATION_STATUS_", "").toLowerCase();
      const { label, variant } = formatInvitationStatus(raw);
      return <StatusBadge label={label} variant={variant} />;
    },
  },
  {
    key: "expiresAt",
    label: "Expires",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
];

export default function InvitationsPage() {
  const [orgId, setOrgId] = useState("");
  const { data: invitations = [], isLoading } = useInvitations(orgId || null);
  const revokeInvitation = useRevokeInvitation();

  const actionsColumn: Column<InvRow> = {
    key: "id",
    label: "Actions",
    render: (_, row) =>
      (row.status as string) === "INVITATION_STATUS_PENDING" ? (
        <button
          onClick={() => revokeInvitation.mutate(row.id as string)}
          className="text-red-600 hover:text-red-800 text-sm font-medium"
        >
          Revoke
        </button>
      ) : null,
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Invitations</h2>
        <OrgSelector value={orgId} onChange={setOrgId} />
      </div>
      <DataTable
        columns={[...columns, actionsColumn]}
        data={invitations as InvRow[]}
        isLoading={orgId ? isLoading : false}
        emptyMessage={orgId ? "No invitations" : "Select an organization"}
        getRowKey={(row) => row.id as string}
      />
    </div>
  );
}
