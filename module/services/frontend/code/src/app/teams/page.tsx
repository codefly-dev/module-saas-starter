"use client";

import { useState } from "react";
import {
  useTeams,
  useCreateTeam,
  useTeamMembers,
  useAddTeamMember,
  useRemoveTeamMember,
} from "@/lib/hooks";
import { DataTable, type Column } from "@/components/data-table";
import { OrgSelector } from "@/components/org-selector";
import { formatDate, truncateUUID } from "@/lib/admin-core";

type TeamRow = Record<string, unknown>;
type MemberRow = Record<string, unknown>;

const teamColumns: Column<TeamRow>[] = [
  { key: "name", label: "Name" },
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
    render: (v) => (
      <span className="text-gray-500">{formatDate(v as string)}</span>
    ),
  },
];

const memberColumns: Column<MemberRow>[] = [
  {
    key: "userId",
    label: "User ID",
    render: (v) => (
      <span className="font-mono text-xs">{truncateUUID(v as string)}</span>
    ),
  },
  {
    key: "joinedAt",
    label: "Joined",
    render: (v) => (
      <span className="text-gray-500">{formatDate(v as string)}</span>
    ),
  },
];

export default function TeamsPage() {
  const [orgId, setOrgId] = useState("");
  const [newTeamName, setNewTeamName] = useState("");
  const [selectedTeamId, setSelectedTeamId] = useState<string | null>(null);
  const [newMemberUserId, setNewMemberUserId] = useState("");

  const { data: teams = [], isLoading } = useTeams(orgId || null);
  const createTeam = useCreateTeam();
  const { data: members = [], isLoading: membersLoading } = useTeamMembers(
    selectedTeamId
  );
  const addMember = useAddTeamMember();
  const removeMember = useRemoveTeamMember();

  const handleCreateTeam = () => {
    if (!orgId || !newTeamName.trim()) return;
    createTeam.mutate(
      { orgId, name: newTeamName.trim() },
      { onSuccess: () => setNewTeamName("") }
    );
  };

  const handleAddMember = () => {
    if (!selectedTeamId || !newMemberUserId.trim()) return;
    addMember.mutate(
      { teamId: selectedTeamId, userId: newMemberUserId.trim() },
      { onSuccess: () => setNewMemberUserId("") }
    );
  };

  const viewColumn: Column<TeamRow> = {
    key: "id",
    label: "Actions",
    render: (_, row) => (
      <button
        onClick={(e) => {
          e.stopPropagation();
          setSelectedTeamId(row.id as string);
        }}
        className="text-blue-600 hover:text-blue-800 text-sm font-medium"
      >
        View Members
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
          if (selectedTeamId) {
            removeMember.mutate({
              teamId: selectedTeamId,
              userId: row.userId as string,
            });
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
        <h2 className="text-2xl font-bold">Teams</h2>
        <OrgSelector value={orgId} onChange={setOrgId} />
      </div>

      {orgId && (
        <div className="flex items-center gap-2 mb-6">
          <input
            type="text"
            placeholder="New team name..."
            value={newTeamName}
            onChange={(e) => setNewTeamName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && handleCreateTeam()}
            className="px-4 py-2 border border-gray-300 dark:border-gray-700 dark:bg-gray-800 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          <button
            onClick={handleCreateTeam}
            disabled={createTeam.isPending || !newTeamName.trim()}
            className="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm font-medium hover:bg-blue-700 disabled:opacity-50"
          >
            {createTeam.isPending ? "Creating..." : "Create Team"}
          </button>
        </div>
      )}

      <DataTable
        columns={[...teamColumns, viewColumn]}
        data={teams as TeamRow[]}
        isLoading={orgId ? isLoading : false}
        emptyMessage={orgId ? "No teams" : "Select an organization"}
        getRowKey={(row) => row.id as string}
      />

      {selectedTeamId && (
        <div className="mt-8">
          <div className="flex items-center justify-between mb-4">
            <h3 className="text-lg font-semibold">
              Team Members{" "}
              <span className="text-gray-500 text-sm font-normal">
                ({truncateUUID(selectedTeamId)})
              </span>
            </h3>
            <button
              onClick={() => setSelectedTeamId(null)}
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
            emptyMessage="No members in this team"
            getRowKey={(row) => row.userId as string}
          />
        </div>
      )}
    </div>
  );
}
