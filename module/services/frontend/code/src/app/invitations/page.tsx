"use client";

import { useState } from "react";
import { useInvitations, useRevokeInvitation, useCreateInvitation } from "@/lib/hooks";
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
  const [showForm, setShowForm] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("member");
  const { data: invitations = [], isLoading } = useInvitations(orgId || null);
  const revokeInvitation = useRevokeInvitation();
  const createInvitation = useCreateInvitation();

  const handleCreate = () => {
    if (!orgId || !email.trim()) return;
    createInvitation.mutate(
      { orgId, email: email.trim(), role },
      {
        onSuccess: () => {
          setEmail("");
          setRole("member");
          setShowForm(false);
        },
      }
    );
  };

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
        <div className="flex items-center gap-3">
          {orgId && (
            <button
              onClick={() => setShowForm(!showForm)}
              className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700"
            >
              {showForm ? "Cancel" : "Invite"}
            </button>
          )}
          <OrgSelector value={orgId} onChange={setOrgId} />
        </div>
      </div>
      {showForm && orgId && (
        <div className="mb-6 p-4 border border-gray-200 dark:border-gray-800 rounded-lg flex items-end gap-3">
          <div className="flex-1">
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Email
            </label>
            <input
              type="email"
              placeholder="user@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="w-full px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1">
              Role
            </label>
            <select
              value={role}
              onChange={(e) => setRole(e.target.value)}
              className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              <option value="member">Member</option>
              <option value="admin">Admin</option>
              <option value="owner">Owner</option>
            </select>
          </div>
          <button
            onClick={handleCreate}
            disabled={createInvitation.isPending || !email.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
          >
            {createInvitation.isPending ? "Sending..." : "Send Invite"}
          </button>
        </div>
      )}
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
