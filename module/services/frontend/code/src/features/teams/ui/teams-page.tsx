"use client";

import { useState, useCallback } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { toast } from "sonner";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Button } from "@/shared/ui";
import { OrgSelector } from "@/components/org-selector";
import { teamQueries } from "../service/queries";
import { teamMutations } from "../service/mutations";
import type { Team } from "../model/types";
import { TeamsTable } from "./teams-table";
import { TeamForm } from "./team-form";
import { TeamMembersPanel } from "./team-members-panel";

export function TeamsPage() {
  const queryClient = useQueryClient();
  const [orgId, setOrgId] = useState("");
  const [showCreate, setShowCreate] = useState(false);
  const [selectedTeam, setSelectedTeam] = useState<Team | null>(null);

  // --- queries ---
  const { data: raw, isLoading } = useQuery({
    ...teamQueries.list(orgId),
    enabled: !!orgId,
  });
  const teams: Team[] = (raw?.teams ?? []).map((t) => ({
    id: t.id,
    orgId: t.orgId,
    name: t.name,
    description: t.description,
    createdAt: t.createdAt ? timestampDate(t.createdAt).toISOString() : undefined,
  }));

  // --- mutations ---
  const createMutation = useMutation({
    mutationFn: ({ name, description }: { name: string; description?: string }) =>
      teamMutations.create(orgId, name, description),
    onSuccess: () => {
      toast.success("Team created");
      queryClient.invalidateQueries({ queryKey: ["teams", orgId] });
      setShowCreate(false);
    },
    onError: () => toast.error("Failed to create team"),
  });

  const handleViewMembers = useCallback((team: Team) => {
    setSelectedTeam(team);
  }, []);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold tracking-tight">Teams</h2>
        <div className="flex items-center gap-3">
          <OrgSelector value={orgId} onChange={setOrgId} />
          {orgId && (
            <Button onClick={() => setShowCreate(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Create Team
            </Button>
          )}
        </div>
      </div>

      {!orgId ? (
        <div className="flex h-48 items-center justify-center rounded-md border border-dashed">
          <p className="text-muted-foreground">
            Select an organization to view teams.
          </p>
        </div>
      ) : (
        <TeamsTable
          data={teams}
          isLoading={isLoading}
          onViewMembers={handleViewMembers}
        />
      )}

      {selectedTeam && (
        <TeamMembersPanel
          teamId={selectedTeam.id}
          teamName={selectedTeam.name}
          onClose={() => setSelectedTeam(null)}
        />
      )}

      <TeamForm
        open={showCreate}
        onSubmit={(vals) => createMutation.mutate(vals)}
        onCancel={() => setShowCreate(false)}
        isPending={createMutation.isPending}
      />
    </div>
  );
}
