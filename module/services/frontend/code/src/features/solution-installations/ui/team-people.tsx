"use client";

import type { SetupSubject } from "@codefly-dev/saas-ui/solution";
import { List, ListItem } from "@codefly-dev/ui/layout";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { UserPicker } from "@/components/user-picker";
import { fromTeamRole } from "@/features/teams/model/types";
import { teamMutations } from "@/features/teams/service/mutations";
import { teamQueries } from "@/features/teams/service/queries";
import { Button, ConfirmDialog, ErrorState } from "@/shared/ui";
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
	// The person an administrator asked to remove, until they confirm.
	const [removing, setRemoving] = useState<{
		userId: string;
		name: string;
	} | null>(null);
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
		<div className="space-y-2 text-sm text-foreground">
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
					<List label={`People in ${team.label}`} className="gap-1">
						{people.length === 0 && (
							<ListItem className="text-muted-foreground">Nobody yet.</ListItem>
						)}
						{people.map((member) => (
							<ListItem
								key={member.userId}
								className="items-center"
								actions={
									<Button
										type="button"
										variant="outline"
										size="sm"
										disabled={change.isPending}
										aria-label={`Remove ${member.userEmail || member.userId} from ${team.label}`}
										onClick={() =>
											setRemoving({
												userId: member.userId,
												name: member.userEmail || member.userId,
											})
										}
									>
										Remove
									</Button>
								}
							>
								{member.userEmail || member.userId}
							</ListItem>
						))}
					</List>
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
						<ErrorState
							title={`${team.label}'s people were not changed`}
							detail={hostRefusal(change.error)}
						/>
					)}
				</div>
			)}
			<ConfirmDialog
				open={removing !== null}
				onOpenChange={(next) => !next && setRemoving(null)}
				title={`Remove ${removing?.name ?? ""} from ${team.label}?`}
				description={`They lose every role ${team.label} has at once. What they hold another way stays, and you can add them again.`}
				confirmLabel="Remove from team"
				pendingLabel="Removing…"
				isPending={change.isPending}
				onConfirm={() => {
					if (!removing) return;
					change.mutate(
						{ add: false, userId: removing.userId },
						{ onSettled: () => setRemoving(null) },
					);
				}}
			/>
		</div>
	);
}
