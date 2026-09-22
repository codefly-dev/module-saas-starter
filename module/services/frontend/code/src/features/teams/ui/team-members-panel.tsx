"use client";

import { ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { UserPicker } from "@/components/user-picker";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import type { TeamMembership } from "@/gen/saas/accounts/v1/common_pb";
import { roleLabel } from "../model/transforms";
import { fromTeamRole, toTeamRole, type TeamRole } from "../model/types";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";

export function TeamMembersPanel({
	orgId,
	teamId,
	teamName,
	canManage,
	currentUserId,
}: {
	orgId: string;
	teamId: string;
	teamName: string;
	canManage: boolean;
	currentUserId?: string;
}) {
	const queryClient = useQueryClient();
	const [newUserId, setNewUserId] = useState("");
	const [pickerVersion, setPickerVersion] = useState(0);
	const [newRole, setNewRole] = useState<"member" | "admin">("member");
	const [search, setSearch] = useState("");
	const [action, setAction] = useState<{
		member: TeamMembership;
		role?: TeamRole;
	} | null>(null);
	const { data, isPending, isError, refetch } = useQuery(
		teamQueries.members(teamId),
	);
	const members = data?.members ?? [];
	const visible = members.filter((member) =>
		(member.userEmail || "").toLowerCase().includes(search.toLowerCase()),
	);
	const refresh = () =>
		queryClient.invalidateQueries({ queryKey: ["team-members", teamId] });
	const add = useMutation({
		mutationFn: () =>
			teamMutations.addMember(teamId, newUserId, fromTeamRole(newRole)),
		onSuccess: async () => {
			await refresh();
			setNewUserId("");
			setPickerVersion((version) => version + 1);
			toast.success("Member added");
		},
	});
	const change = useMutation({
		mutationFn: (target: NonNullable<typeof action>) =>
			target.role
				? teamMutations.addMember(
						teamId,
						target.member.userId,
						fromTeamRole(target.role),
					)
				: teamMutations.removeMember(teamId, target.member.userId),
		onSuccess: async (_data, target) => {
			await refresh();
			setAction(null);
			toast.success(target.role ? "Team role updated" : "Member removed");
		},
	});
	function confirm(member: TeamMembership, role?: TeamRole) {
		change.reset();
		setAction({ member, role });
	}
	return (
		<section className="space-y-4" aria-label="Team members">
			<div>
				<h2 className="text-xl font-semibold">
					Members {!isPending && !isError && `(${members.length})`}
				</h2>
				<p className="text-sm text-muted-foreground">
					Team roles apply to {teamName}; they do not change organization roles.
				</p>
			</div>
			{isError ? (
				<div role="alert">
					Couldn&apos;t load team members.{" "}
					<Button onClick={() => void refetch()}>Retry</Button>
				</div>
			) : isPending ? (
				<p>Loading members…</p>
			) : (
				<>
					{canManage && (
						<div className="space-y-2 rounded-lg border p-4">
							<h3 className="font-medium">Add an organization member</h3>
							<div className="flex flex-wrap items-end gap-3">
								<UserPicker
									key={pickerVersion}
									orgId={orgId}
									value={newUserId}
									onChange={setNewUserId}
									exclude={members.map((member) => member.userId)}
								/>
								<div className="space-y-2">
									<Label htmlFor="new-team-role">Team role</Label>
									<Select
										items={{ member: "Member", admin: "Admin" }}
										value={newRole}
										onValueChange={(value) => {
											if (value === "member" || value === "admin")
												setNewRole(value);
										}}
									>
										<SelectTrigger id="new-team-role">
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="member">Member</SelectItem>
											<SelectItem value="admin">Admin</SelectItem>
										</SelectContent>
									</Select>
								</div>
								<Button
									disabled={!newUserId || add.isPending || change.isPending}
									onClick={() => {
										if (canManage) add.mutate();
									}}
								>
									{add.isPending ? "Adding…" : "Add member"}
								</Button>
							</div>
							{add.isError && (
								<p role="alert" className="text-sm text-destructive">
									Couldn&apos;t add member:{" "}
									{ConnectError.from(add.error).rawMessage}
								</p>
							)}
						</div>
					)}
					<div className="space-y-2">
						<Label htmlFor="team-member-search">Search members</Label>
						<Input
							id="team-member-search"
							placeholder="Search by email…"
							value={search}
							onChange={(event) => setSearch(event.target.value)}
							className="max-w-sm"
						/>
					</div>
					<div className="overflow-x-auto rounded-lg border">
						<table className="w-full text-sm">
							<thead>
								<tr className="border-b text-left">
									<th className="p-3">Member</th>
									<th className="p-3">Team role</th>
									<th className="p-3">Joined</th>
									{canManage && <th className="p-3">Actions</th>}
								</tr>
							</thead>
							<tbody>
								{visible.map((member) => {
									const role = toTeamRole(member.role);
									const label = member.userEmail || "User unavailable";
									return (
										<tr key={member.userId} className="border-b last:border-0">
											<td className="p-3">
												{label}
												{member.userId === currentUserId && (
													<span className="ml-2 text-muted-foreground">
														(you)
													</span>
												)}
											</td>
											<td className="p-3">
												<Badge
													variant={role === "member" ? "outline" : "secondary"}
												>
													{roleLabel(role)}
												</Badge>
											</td>
											<td className="p-3 text-muted-foreground">
												{formatDate(member.joinedAt)}
											</td>
											{canManage && (
												<td className="p-3">
													<div className="flex flex-wrap gap-2">
														{role !== "owner" && (
															<Button
																variant="outline"
																size="sm"
																disabled={change.isPending}
																aria-label={`${role === "admin" ? "Make member" : "Make admin"}: ${label}`}
																onClick={() =>
																	confirm(
																		member,
																		role === "admin" ? "member" : "admin",
																	)
																}
															>
																{role === "admin"
																	? "Make member"
																	: "Make admin"}
															</Button>
														)}
														<Button
															variant="outline"
															size="sm"
															disabled={change.isPending}
															aria-label={`Remove ${label}`}
															onClick={() => confirm(member)}
														>
															Remove
														</Button>
													</div>
												</td>
											)}
										</tr>
									);
								})}
								{visible.length === 0 && (
									<tr>
										<td
											className="p-8 text-center text-muted-foreground"
											colSpan={canManage ? 4 : 3}
										>
											{members.length
												? "No matching members."
												: "No members in this team yet."}
										</td>
									</tr>
								)}
							</tbody>
						</table>
					</div>
				</>
			)}
			<Dialog
				open={!!action}
				onOpenChange={(open) => {
					if (!open && !change.isPending) setAction(null);
				}}
			>
				<DialogContent>
					<DialogHeader>
						<DialogTitle>
							{action?.role ? "Change team role?" : "Remove team member?"}
						</DialogTitle>
						<DialogDescription>
							{action?.role
								? `Change ${action.member.userEmail || "this user"} to ${roleLabel(action.role).toLowerCase()} in ${teamName}.`
								: `Remove ${action?.member.userEmail || "this user"} from ${teamName}. Their organization membership is unchanged.`}
							{action?.member.userId === currentUserId &&
								" This changes your own team access."}
						</DialogDescription>
					</DialogHeader>
					{change.isError && (
						<p role="alert" className="text-sm text-destructive">
							{ConnectError.from(change.error).rawMessage}
						</p>
					)}
					<DialogFooter>
						<Button
							variant="outline"
							disabled={change.isPending}
							onClick={() => setAction(null)}
						>
							Cancel
						</Button>
						<Button
							disabled={change.isPending || !canManage}
							onClick={() => {
								if (action && canManage) change.mutate(action);
							}}
						>
							{change.isPending
								? "Saving…"
								: action?.role
									? "Change role"
									: "Remove member"}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</section>
	);
}
