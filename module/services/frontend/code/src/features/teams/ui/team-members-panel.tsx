"use client";

import { failureText } from "@/shared/lib/failure-text";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { DataTable } from "@/shared/ui/data-table";
import { useState } from "react";
import { toast } from "sonner";
import { UserPicker } from "@/components/user-picker";
import type { TeamMembership } from "@/gen/saas/accounts/v1/common_pb";
import { readOutcome, readOutcomeMessage } from "@/shared/lib/read-outcome";
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
	Spinner,
	Panel,
	ErrorState,
	useLoadingPhase,
} from "@/shared/ui";
import { roleLabel } from "../model/transforms";
import { fromTeamRole, type TeamRole, toTeamRole } from "../model/types";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";

export function addMemberErrorMessage(error: unknown): string {
	return failureText("add this member", error);
}

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
	const { data, isPending, isError, error, isFetching, refetch } = useQuery(
		teamQueries.members(teamId),
	);
	// Nothing at all for the first 200ms, then an indicator that stays long enough
	// to read. The `quiet` window must render nothing rather than fall through:
	// below it sits the roster, whose own empty row would otherwise say "No
	// members in this team yet." about a team still being read.
	const { indicator, quiet } = useLoadingPhase(isPending);
	// A refresh of a roster already on screen, as distinct from the first read.
	const { indicator: refreshingIndicator } = useLoadingPhase(
		isFetching && !isPending && !isError,
	);
	const members = data?.members ?? [];
	const visible = members.filter((member) =>
		(member.userEmail || "").toLowerCase().includes(search.toLowerCase()),
	);
	// Not awaited, deliberately. React Query holds a mutation `isPending` until
	// its `onSuccess` resolves, and `invalidateQueries` resolves only once the
	// refetch it triggers has settled — so awaiting it here would make the
	// button's "Adding…" label, and this whole disabled form, report the roster
	// *read* that follows the write instead of the write itself. A slow read then
	// leaves the button stuck on "Adding…" beside the pre-add count with the
	// membership already written, which is the reported symptom in #964.
	const refresh = () => {
		void queryClient.invalidateQueries({
			queryKey: ["team-members", teamId],
		});
	};
	const add = useMutation({
		mutationFn: () =>
			teamMutations.addMember(teamId, newUserId, fromTeamRole(newRole)),
		onSuccess: () => {
			setNewUserId("");
			setPickerVersion((version) => version + 1);
			toast.success("Member added");
			refresh();
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
		onSuccess: (_data, target) => {
			setAction(null);
			toast.success(target.role ? "Team role updated" : "Member removed");
			refresh();
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
					{/* The count is the cached roster's, and after a write that roster is
				    behind the database until the refresh lands — which is how a
				    successful add sat beside "(0)" (#964). Saying so beats hiding the
				    number: blanking it on every background refetch is its own flicker,
				    and a read that never returns would blank it for good. Gated by the
				    same 200/300 rule as every other indicator, so a refresh that lands
				    quickly says nothing at all. */}
					{refreshingIndicator && (
						<Spinner label="Refreshing members" size="sm" className="ml-2" />
					)}
				</h2>
				<p className="text-sm text-muted-foreground">
					Team roles apply to {teamName}; they do not change organization roles.
				</p>
			</div>
			{isError ? (
				<Panel className="space-y-3">
					<ErrorState
						title="Couldn't load team members"
						detail={readOutcomeMessage(
							readOutcome(true, error),
							"this team's members",
							"",
						)}
					/>
					{readOutcome(true, error) !== "forbidden" && (
						<Button variant="outline" onClick={() => void refetch()}>
							Retry
						</Button>
					)}
				</Panel>
			) : quiet ? null : indicator ? (
				<Spinner label="Loading members" />
			) : (
				<>
					{canManage && (
						<Panel className="space-y-3">
							<h3 className="font-medium">Add an organization member</h3>
							<div className="grid items-start gap-4 sm:grid-cols-[minmax(0,1fr)_auto_auto]">
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
									{addMemberErrorMessage(add.error)}
								</p>
							)}
						</Panel>
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
					<MemberTable
						members={visible}
						currentUserId={currentUserId}
						canManage={canManage}
						pending={change.isPending}
						onAction={confirm}
						emptyMessage={
							members.length
								? "No matching members. Try a different search."
								: "No members in this team yet. Add an organization member above to get started."
						}
					/>
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
							{failureText("update this team membership", change.error)}
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

const memberColumn = createColumnHelper<TeamMembership>();
function MemberTable({
	members,
	currentUserId,
	canManage,
	pending,
	onAction,
	emptyMessage,
}: {
	members: TeamMembership[];
	currentUserId?: string;
	canManage: boolean;
	pending: boolean;
	onAction: (member: TeamMembership, role?: TeamRole) => void;
	emptyMessage: string;
}) {
	const columns = [
		memberColumn.accessor("userEmail", {
			header: "Member",
			cell: ({ row }) => (
				<span>
					{row.original.userEmail || "User unavailable"}
					{row.original.userId === currentUserId && (
						<span className="ml-2 text-muted-foreground">(you)</span>
					)}
				</span>
			),
		}),
		memberColumn.accessor("role", {
			header: "Team role",
			cell: ({ getValue }) => (
				<Badge
					variant={
						toTeamRole(getValue()) === "member" ? "outline" : "secondary"
					}
				>
					{roleLabel(toTeamRole(getValue()))}
				</Badge>
			),
		}),
		memberColumn.accessor("joinedAt", {
			header: "Joined",
			cell: ({ getValue }) => formatDate(getValue()),
		}),
		...(canManage
			? [
					memberColumn.display({
						id: "actions",
						header: "Actions",
						cell: ({ row }) => {
							const member = row.original;
							const role = toTeamRole(member.role);
							const label = member.userEmail || "User unavailable";
							return (
								<div className="flex flex-wrap gap-2">
									{role !== "owner" && (
										<Button
											variant="outline"
											size="sm"
											disabled={pending}
											aria-label={`${role === "admin" ? "Make member" : "Make admin"}: ${label}`}
											onClick={() =>
												onAction(member, role === "admin" ? "member" : "admin")
											}
										>
											{role === "admin" ? "Make member" : "Make admin"}
										</Button>
									)}
									<Button
										variant="outline"
										size="sm"
										disabled={pending}
										aria-label={`Remove ${label}`}
										onClick={() => onAction(member)}
									>
										Remove
									</Button>
								</div>
							);
						},
					}),
				]
			: []),
	];
	const table = useReactTable({
		data: members,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} emptyMessage={emptyMessage} />;
}
