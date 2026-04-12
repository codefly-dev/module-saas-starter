"use client";

import { useState } from "react";
import {
  useOrganizations,
  useCreateOrganization,
  useOrgMembers,
  useAddOrgMember,
  useRemoveOrgMember,
} from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { formatDate, truncateUUID } from "@/lib/admin-core";

type OrgRow = Record<string, unknown>;
type MemberRow = Record<string, unknown>;

const columns: Column<OrgRow>[] = [
  { key: "name", label: "Name" },
  { key: "slug", label: "Slug" },
  {
    key: "id",
    label: "ID",
    render: (v) => (
      <span className="font-mono text-xs text-gray-500">
        {truncateUUID(v as string)}
      </span>
    ),
  },
  {
    key: "createdAt",
    label: "Created",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
];

const memberColumns: Column<MemberRow>[] = [
  {
    key: "userId",
    label: "User ID",
    render: (v) => <span className="font-mono text-xs">{truncateUUID(v as string)}</span>,
  },
  {
    key: "role",
    label: "Role",
    render: (v) => <span className="text-gray-500">{v === 0 ? "Owner" : v === 1 ? "Admin" : v === 2 ? "Member" : String(v ?? "Member")}</span>,
  },
  {
    key: "joinedAt",
    label: "Joined",
    render: (v) => <span className="text-gray-500">{formatDate(v as string)}</span>,
  },
];

export default function OrganizationsPage() {
  const [newName, setNewName] = useState("");
  const [selectedOrgId, setSelectedOrgId] = useState<string | null>(null);
  const [newMemberUserId, setNewMemberUserId] = useState("");
  const [newMemberRole, setNewMemberRole] = useState(1);

  const { data: orgs = [], isLoading } = useOrganizations();
  const createOrg = useCreateOrganization();
  const { data: members = [], isLoading: membersLoading } = useOrgMembers(selectedOrgId);
  const addMember = useAddOrgMember();
  const removeMember = useRemoveOrgMember();

  const handleCreate = () => {
    if (!newName.trim()) return;
    createOrg.mutate(newName.trim(), {
      onSuccess: () => setNewName(""),
    });
  };

  const handleAddMember = () => {
    if (!selectedOrgId || !newMemberUserId.trim()) return;
    addMember.mutate(
      { orgId: selectedOrgId, userId: newMemberUserId.trim(), role: newMemberRole },
      { onSuccess: () => setNewMemberUserId("") },
    );
  };

  const viewMembersColumn: Column<OrgRow> = {
    key: "id",
    label: "Actions",
    render: (_, row) => (
      <button
        onClick={(e) => {
          e.stopPropagation();
          setSelectedOrgId(row.id as string);
        }}
        className="text-blue-600 hover:text-blue-800 text-sm font-medium"
      >
        Members
      </button>
    ),
  };

  const removeMemberColumn: Column<MemberRow> = {
    key: "userId",
    label: "Actions",
    render: (_, row) => (
      <button
        onClick={(e) => {
          e.stopPropagation();
          if (selectedOrgId) {
            removeMember.mutate({ orgId: selectedOrgId, userId: row.userId as string });
          }
        }}
        className="text-red-600 hover:text-red-800 text-sm font-medium"
      >
        Remove
      </button>
    ),
  };

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-2xl font-bold">Organizations</h2>
        <div className="flex items-center gap-2">
          <input
            type="text"
            placeholder="New organization name..."
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && handleCreate()}
            className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          <button
            onClick={handleCreate}
            disabled={createOrg.isPending || !newName.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
          >
            {createOrg.isPending ? "Creating..." : "Create"}
          </button>
        </div>
      </div>
      <DataTable
        columns={[...columns, viewMembersColumn]}
        data={orgs as OrgRow[]}
        isLoading={isLoading}
        emptyMessage="No organizations"
        getRowKey={(row) => row.id as string}
      />

      {selectedOrgId && (
        <div className="mt-8">
          <div className="flex items-center justify-between mb-4">
            <h3 className="text-lg font-semibold">
              Members{" "}
              <span className="text-gray-500 text-sm font-normal">
                ({truncateUUID(selectedOrgId)})
              </span>
            </h3>
            <button
              onClick={() => setSelectedOrgId(null)}
              className="text-gray-500 hover:text-gray-700 text-sm"
            >
              Close
            </button>
          </div>

          <div className="flex items-center gap-2 mb-4">
            <input
              type="text"
              placeholder="User ID to add..."
              value={newMemberUserId}
              onChange={(e) => setNewMemberUserId(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && handleAddMember()}
              className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
            <select
              value={newMemberRole}
              onChange={(e) => setNewMemberRole(Number(e.target.value))}
              className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
            >
              <option value={0}>Owner</option>
              <option value={1}>Admin</option>
              <option value={2}>Member</option>
            </select>
            <button
              onClick={handleAddMember}
              disabled={addMember.isPending || !newMemberUserId.trim()}
              className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
            >
              {addMember.isPending ? "Adding..." : "Add Member"}
            </button>
          </div>

          <DataTable
            columns={[...memberColumns, removeMemberColumn]}
            data={members as MemberRow[]}
            isLoading={membersLoading}
            emptyMessage="No members in this organization"
            getRowKey={(row) => row.userId as string}
          />
        </div>
      )}
    </div>
  );
}
