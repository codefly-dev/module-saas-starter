"use client";

import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Search, UserPlus, Users } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
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
	InputGroup,
	InputGroupAddon,
	InputGroupInput,
	Label,
	Section,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Spinner,
	Stack,
	Surface,
	Table,
	TableBody,
	TableCell,
	TableEmptyState,
	TableHead,
	TableHeader,
	TableRow,
	TableToolbar,
	useLoadingPhase,
} from "@/shared/ui";
import { roleLabel } from "../model/transforms";
import { fromTeamRole, type TeamRole, toTeamRole } from "../model/types";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";

export function addMemberErrorMessage(error: unknown): string {
	const connectError = ConnectError.from(error);
	if (
		connectError.code === Code.FailedPrecondition &&
		connectError.rawMessage
	) {
		return connectError.rawMessage;
	}
	return "Failed to add member";
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
	const [adding, setAdding] = useState(false);
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
	// below it sits the roster, whose own empty state would otherwise say "No
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
	const refresh = () => {
		void queryClient.invalidateQueries({
			queryKey: ["team-members", teamId],
		});
	};
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
	// One control, rendered in whichever place is the reader's next step: beside
	// the heading once there is a roster, and inside the empty state when there
	// is not — an empty roster and the thing that fills it were two separate
	// blocks, with the remedy stacked above the "No members" it answers.
	const addMember = canManage ? (
		<Button onClick={() => setAdding(true)}>
			<UserPlus />
			Add member
		</Button>
	) : null;
	const settled = !isPending && !isError;
	return (
		<Section
			aria-label="Team members"
			title={
				<>
					Members {settled && `(${members.length})`}
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
				</>
			}
			description={`Team roles apply to ${teamName}; they do not change organization roles.`}
			actions={settled && members.length > 0 ? addMember : undefined}
		>
			{isError ? (
				<div role="alert">
					{readOutcomeMessage(
						readOutcome(true, error),
						"this team's members",
						"",
					)}{" "}
					<Button variant="outline" onClick={() => void refetch()}>
						Retry
					</Button>
				</div>
			) : quiet ? null : indicator ? (
				<Spinner label="Loading members" />
			) : members.length === 0 ? (
				<EmptyState
					icon={Users}
					title="No members in this team yet."
					description={
						canManage
							? "Add someone who is already in this organization. Their organization role is unchanged."
							: "A team or organization administrator adds members."
					}
					action={addMember ?? undefined}
				/>
			) : (
				<Stack gap={3}>
					<TableToolbar className="py-0">
						<InputGroup className="max-w-xs">
							<InputGroupAddon>
								<Search />
							</InputGroupAddon>
							<InputGroupInput
								id="team-member-search"
								aria-label="Search members"
								placeholder="Search by email…"
								value={search}
								onChange={(event) => setSearch(event.target.value)}
							/>
						</InputGroup>
					</TableToolbar>
					<Surface className="overflow-hidden">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead className="px-4">Member</TableHead>
									<TableHead>Team role</TableHead>
									<TableHead>Joined</TableHead>
									{canManage && (
										<TableHead className="px-4 text-right">Actions</TableHead>
									)}
								</TableRow>
							</TableHeader>
							<TableBody>
								{visible.map((member) => {
									const role = toTeamRole(member.role);
									const label = member.userEmail || "User unavailable";
									return (
										<TableRow key={member.userId}>
											<TableCell className="px-4 font-medium">
												{label}
												{member.userId === currentUserId && (
													<span className="ml-2 font-normal text-muted-foreground">
														(you)
													</span>
												)}
											</TableCell>
											<TableCell>
												<Badge
													variant={role === "member" ? "outline" : "secondary"}
												>
													{roleLabel(role)}
												</Badge>
											</TableCell>
											<TableCell className="text-muted-foreground">
												{formatDate(member.joinedAt)}
											</TableCell>
											{canManage && (
												<TableCell className="px-4">
													<Stack
														direction="row"
														gap={2}
														justify="end"
														className="flex-wrap"
													>
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
															variant="destructive"
															size="sm"
															disabled={change.isPending}
															aria-label={`Remove ${label}`}
															onClick={() => confirm(member)}
														>
															Remove
														</Button>
													</Stack>
												</TableCell>
											)}
										</TableRow>
									);
								})}
								{visible.length === 0 && (
									<TableEmptyState colSpan={canManage ? 4 : 3}>
										No matching members.
									</TableEmptyState>
								)}
							</TableBody>
						</Table>
					</Surface>
				</Stack>
			)}
			{canManage && (
				<AddTeamMember
					open={adding}
					onOpenChange={setAdding}
					orgId={orgId}
					teamId={teamId}
					teamName={teamName}
					canManage={canManage}
					exclude={members.map((member) => member.userId)}
					onAdded={refresh}
				/>
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
		</Section>
	);
}

// The add form, in the overlay every other write on this surface already uses
// (create team, rename, change role, remove). Inline, it was a full-width card
// above the roster carrying a second email search beside the roster's own, and
// a primary button that is disabled until a user is picked — so the page's one
// call to action was permanently greyed out, which is what "the buttons look
// disabled" was.
function AddTeamMember({
	open,
	onOpenChange,
	orgId,
	teamId,
	teamName,
	canManage,
	exclude,
	onAdded,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	orgId: string;
	teamId: string;
	teamName: string;
	canManage: boolean;
	exclude: string[];
	onAdded: () => void;
}) {
	const [userId, setUserId] = useState("");
	const [role, setRole] = useState<"member" | "admin">("member");
	const add = useMutation({
		mutationFn: () =>
			teamMutations.addMember(teamId, userId, fromTeamRole(role)),
		// Closed on the write, and `onAdded` is not awaited — deliberately. React
		// Query holds a mutation `isPending` until its `onSuccess` resolves, and
		// `invalidateQueries` resolves only once the refetch it triggers has
		// settled, so awaiting it here would make this dialog's "Adding…" report
		// the roster *read* that follows the write instead of the write itself. A
		// slow read then leaves the dialog open on "Adding…" beside the pre-add
		// count with the membership already written, which is #964.
		onSuccess: () => {
			setUserId("");
			setRole("member");
			onOpenChange(false);
			toast.success("Member added");
			onAdded();
		},
	});
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!add.isPending) {
					if (!next) add.reset();
					onOpenChange(next);
				}
			}}
		>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Add an organization member</DialogTitle>
					<DialogDescription>
						{`Only people already in this organization can join ${teamName}. Their organization role is unchanged.`}
					</DialogDescription>
				</DialogHeader>
				<Stack gap={4}>
					{/* Remounted per opening, so a previous pick is not still selected. */}
					<UserPicker
						key={open ? "open" : "closed"}
						orgId={orgId}
						value={userId}
						onChange={setUserId}
						exclude={exclude}
					/>
					<Stack gap={2}>
						<Label htmlFor="new-team-role">Team role</Label>
						<Select
							items={{ member: "Member", admin: "Admin" }}
							value={role}
							onValueChange={(value) => {
								if (value === "member" || value === "admin") setRole(value);
							}}
						>
							<SelectTrigger id="new-team-role" className="w-full">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="member">Member</SelectItem>
								<SelectItem value="admin">Admin</SelectItem>
							</SelectContent>
						</Select>
					</Stack>
					{add.isError && (
						<p role="alert" className="text-sm text-destructive">
							Couldn&apos;t add member: {addMemberErrorMessage(add.error)}
						</p>
					)}
				</Stack>
				<DialogFooter>
					<Button
						variant="outline"
						disabled={add.isPending}
						onClick={() => onOpenChange(false)}
					>
						Cancel
					</Button>
					<Button
						disabled={!userId || add.isPending || !canManage}
						onClick={() => {
							if (canManage) add.mutate();
						}}
					>
						{add.isPending ? "Adding…" : "Add to team"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
