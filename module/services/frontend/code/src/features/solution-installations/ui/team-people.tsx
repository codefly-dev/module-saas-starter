"use client";

import type { SetupSubject } from "@codefly-dev/saas-ui/solution";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { UserPicker } from "@/components/user-picker";
import { fromTeamRole } from "@/features/teams/model/types";
import { teamMutations } from "@/features/teams/service/mutations";
import { teamQueries } from "@/features/teams/service/queries";
import { Button } from "@/shared/ui";
import { hostRefusal } from "../model/installations";

/**
 * The people of a team holding a role, where the role is read: how many, and
 * — opened — each one removable and anyone in the organization added. Adding
 * a person to the team is how they get the role; the team's own page keeps
 * every other team setting.
 */
export function TeamPeople({
	orgId,
	team,
}: {
	orgId: string;
	team: SetupSubject;
}) {
	const queryClient = useQueryClient();
	const members = useQuery(teamQueries.members(team.id));
	const [open, setOpen] = useState(false);
	const [person, setPerson] = useState("");
	const [picker, setPicker] = useState(0);
	const refresh = () =>
		void queryClient.invalidateQueries({ queryKey: ["team-members", team.id] });
	const change = useMutation({
		mutationFn: ({ add, userId }: { add: boolean; userId: string }) =>
			add
				? teamMutations.addMember(team.id, userId, fromTeamRole("member"))
				: teamMutations.removeMember(team.id, userId),
		onSuccess: (_, { add }) => {
			if (add) {
				setPerson("");
				setPicker((n) => n + 1);
			}
			refresh();
		},
	});
	const people = members.data?.members ?? [];
	const count = members.isSuccess
		? `${people.length} ${people.length === 1 ? "person" : "people"}`
		: members.isError
			? "People could not be read"
			: "…";
	return (
		<div className="space-y-2 text-sm">
			<div className="flex flex-wrap items-center gap-2">
				<span className="text-muted-foreground">{count}</span>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					aria-expanded={open}
					aria-label={`${open ? "Close" : "Add or remove"} people in ${team.label}`}
					onClick={() => setOpen(!open)}
				>
					{open ? "Done" : "Add or remove people"}
				</Button>
			</div>
			{open && (
				<div className="space-y-2 rounded-md border bg-background p-2">
					<ul aria-label={`People in ${team.label}`} className="space-y-1">
						{people.length === 0 && (
							<li className="text-muted-foreground">Nobody yet.</li>
						)}
						{people.map((member) => (
							<li
								key={member.userId}
								className="flex items-center justify-between gap-2"
							>
								<span>{member.userEmail || member.userId}</span>
								<Button
									type="button"
									variant="outline"
									size="sm"
									disabled={change.isPending}
									aria-label={`Remove ${member.userEmail || member.userId} from ${team.label}`}
									onClick={() =>
										change.mutate({ add: false, userId: member.userId })
									}
								>
									Remove
								</Button>
							</li>
						))}
					</ul>
					<div className="flex flex-wrap items-end gap-2">
						<UserPicker
							key={picker}
							orgId={orgId}
							value={person}
							onChange={setPerson}
							exclude={people.map((member) => member.userId)}
						/>
						<Button
							type="button"
							size="sm"
							disabled={!person || change.isPending}
							onClick={() => change.mutate({ add: true, userId: person })}
						>
							Add to {team.label}
						</Button>
					</div>
					{change.isError && (
						<p role="alert" className="text-destructive">
							{hostRefusal(change.error)}
						</p>
					)}
				</div>
			)}
		</div>
	);
}
