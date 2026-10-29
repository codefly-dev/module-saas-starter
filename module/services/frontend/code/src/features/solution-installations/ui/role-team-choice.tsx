"use client";
import { SolutionRoleGrantForm } from "@codefly-dev/saas-ui/solution";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { TeamForm } from "@/features/teams/ui/team-form";
import { teamMutations } from "@/features/teams/service/mutations";
import { teamQueries } from "@/features/teams/service/queries";
import type { CreateTeamValues } from "@/features/teams/model/schemas";
import type { Team } from "@/gen/saas/accounts/v1/common_pb";
import { Button } from "@/shared/ui";
import { hostRefusal } from "../model/installations";

/** Existing organization team creation and Setup picker, scoped to one declared role. */
export function RoleTeamChoice({ orgId, solution, role, teams, disabled, onChoose, onWorking }: {
 orgId: string; solution: string; role: string; teams: Team[]; disabled: boolean;
 onChoose: (team: Team) => void; onWorking: (working: boolean) => void;
}) {
 const client = useQueryClient();
 const [creating, setCreating] = useState(false);
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState<string>();
 const [collision, setCollision] = useState<Team>();
 const suggestedName = `${solution}-${role}`.toLowerCase().replace(/\s+/g, "-");
 const create = async (values: CreateTeamValues) => {
  if (busy) return;
  setBusy(true); onWorking(true); setError(undefined); setCollision(undefined);
  try {
   // Read immediately before creation, including after a lost response. Never silently reuse.
   const current = await client.fetchQuery({...teamQueries.list(orgId), staleTime: 0});
   const found = current.teams.find((team) => team.name.trim().toLowerCase() === values.name.trim().toLowerCase());
   if (found) { setCollision(found); setError("A team with this name already exists in this organization. Choose it explicitly or change the name."); return; }
   const answer = await teamMutations.create(orgId, values.name, values.description);
   if (!answer.team) throw new Error("The host did not return the created team. Refresh teams before trying again.");
   onChoose(answer.team);
   setCreating(false);
   await client.invalidateQueries({queryKey: teamQueries.list(orgId).queryKey});
  } catch (reason) {
   setError(hostRefusal(reason));
   // The next explicit attempt re-reads names before it can create again.
  } finally { setBusy(false); onWorking(false); }
 };
 return <div className="rounded border p-3 space-y-2" aria-label={`Team for ${role}`}>
  <h4 className="font-medium">{role}</h4>
  <SolutionRoleGrantForm fixedRole={role} roles={[role]} subjects={teams.map((team) => ({kind:"team",id:team.id,label:team.name}))} allowUnlistedSubjects={false} subjectLabel="Team" submitLabel={`Use team for ${role}`} busy={disabled} onGrant={({teamId}) => {
   const team = teams.find((entry) => entry.id === teamId); if (team) onChoose(team);
  }} />
  <Button type="button" variant="outline" disabled={disabled} onClick={() => {setError(undefined);setCollision(undefined);setCreating(true);}}>Create team for {role}</Button>
  {creating && <TeamForm open initial={{name:suggestedName}} isPending={busy} error={error} onSubmit={(values) => void create(values)} onCancel={() => setCreating(false)} existingTeam={collision ? {name:collision.name,onUse:() => {onChoose(collision);setCreating(false);}} : undefined} />}
 </div>;
}
