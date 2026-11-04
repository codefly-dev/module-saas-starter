"use client";

import { Plus, ShieldCheck } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import {
	useAssignRole,
	useRevokeRole,
} from "@/features/roles/service/mutations";
import { useRoleAssignments, useRoles } from "@/features/roles/service/queries";
import { SubjectKind } from "@/gen/saas/accounts/v1/common_pb";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Button,
	Chip,
	ChipGroup,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Panel,
	Section,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Spinner,
	Stack,
	useLoadingPhase,
} from "@/shared/ui";

// The roles a team carries reach every member of it, which is why they belong
// on the team page rather than only on each member's.
export function TeamRoles({
	teamId,
	orgId,
	canGrant,
}: {
	teamId: string;
	orgId: string;
	canGrant: boolean;
}) {
	const [granting, setGranting] = useState(false);
	const [picked, setPicked] = useState("");
	const { data: roles = [] } = useRoles(orgId);
	const {
		data: assignments = [],
		isLoading,
		isError,
		error,
	} = useRoleAssignments(orgId, teamId, SubjectKind.TEAM);
	const { indicator, quiet } = useLoadingPhase(isLoading);
	const outcome = readOutcome(isError, error);
	// A refused read takes precedence over badges already on screen: TanStack keeps
	// the last successful answer when a refetch rejects, so testing `length === 0`
	// first would leave a team's grants rendered after the read was refused. A
	// transient failure keeps them, with a note that they may be out of date.
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the roles this team carries");
	const assignRole = useAssignRole();
	const revokeRole = useRevokeRole();

	const roleNameById = new Map(
		roles.map((role) => [role.id, role.name] as const),
	);
	const held = new Set(assignments.map((assignment) => assignment.roleId));
	const available = roles.filter((role) => !held.has(role.id));
	// Which of the two: no roles, or no permission to see them. An unread
	// `isError` here rendered a denied read as "carries no roles", which on a
	// permissions console is a wrong answer, not a blank one.
	const nothingMessage = readOutcomeMessage(
		outcome,
		"the roles this team carries",
		"This team carries no roles.",
	);
	const revoke = (assignment: { roleId: string; scope?: string }) => {
		if (revokeRole.isPending) return;
		revokeRole.mutate(
			{
				subjectId: teamId,
				roleId: assignment.roleId,
				orgId,
				scope: assignment.scope,
			},
			{
				onSuccess: () => toast.success("Role revoked"),
				onError: (error) =>
					toast.error(`Failed to revoke: ${(error as Error).message}`),
			},
		);
	};
	// The section's own action, not a control floating beside the heading with a
	// half-filled Select and a permanently disabled "Grant" next to it: the
	// choice belongs with the confirmation, and the trigger stays pressable.
	const grant = canGrant ? (
		<Button variant="outline" size="sm" onClick={() => setGranting(true)}>
			<Plus />
			Grant a role
		</Button>
	) : null;

	return (
		<Section
			title="Roles"
			description="Every member of this team holds these roles."
			actions={
				!withheld && assignments.length > 0 ? (grant ?? undefined) : undefined
			}
		>
			{quiet ? null : indicator ? (
				<Spinner label="Loading the roles this team carries" size="sm" />
			) : withheld || assignments.length === 0 ? (
				isError ? (
					<Panel>
						<span role="alert" className="text-sm text-muted-foreground">
							{nothingMessage}
						</span>
					</Panel>
				) : (
					<EmptyState
						variant="default"
						icon={ShieldCheck}
						title={nothingMessage}
						description="A role granted here reaches every member of this team."
						action={grant ?? undefined}
					/>
				)
			) : (
				<Panel>
					<Stack gap={2}>
						{stale && (
							<span role="status" className="text-sm text-muted-foreground">
								{stale}
							</span>
						)}
						<ChipGroup label="Roles this team holds">
							{assignments.map((assignment) => {
								const name = `${roleNameById.get(assignment.roleId) ?? "Role unavailable"}${assignment.scope ? ` in ${assignment.scope}` : ""}`;
								// `onRemove` and `removeLabel` are one choice in the kit's props —
								// the remove control has no text of its own — so the chip is
								// written twice rather than spread from a partial that union
								// cannot accept.
								return canGrant ? (
									<Chip
										key={assignment.id}
										className="font-mono"
										render={<Link href={`/admin/roles/${assignment.roleId}`} />}
										removeLabel="Revoke"
										onRemove={() => revoke(assignment)}
									>
										{name}
									</Chip>
								) : (
									<Chip
										key={assignment.id}
										className="font-mono"
										render={<Link href={`/admin/roles/${assignment.roleId}`} />}
									>
										{name}
									</Chip>
								);
							})}
						</ChipGroup>
					</Stack>
				</Panel>
			)}
			{canGrant && (
				<Dialog
					open={granting}
					onOpenChange={(open) => {
						if (!assignRole.isPending) {
							if (!open) setPicked("");
							setGranting(open);
						}
					}}
				>
					<DialogContent>
						<DialogHeader>
							<DialogTitle>Grant a role to this team</DialogTitle>
							<DialogDescription>
								Every member of this team holds it, now and as the roster
								changes.
							</DialogDescription>
						</DialogHeader>
						<Select
							value={picked}
							items={available.map((role) => ({
								value: role.id,
								label: role.name,
							}))}
							onValueChange={(value) => setPicked(value ?? "")}
						>
							<SelectTrigger className="w-full">
								<SelectValue placeholder="Pick a role…" />
							</SelectTrigger>
							<SelectContent>
								{available.map((role) => (
									<SelectItem key={role.id} value={role.id}>
										{role.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<DialogFooter>
							<Button
								variant="outline"
								disabled={assignRole.isPending}
								onClick={() => setGranting(false)}
							>
								Cancel
							</Button>
							<Button
								disabled={!picked || assignRole.isPending}
								onClick={() =>
									assignRole.mutate(
										{
											subjectId: teamId,
											subjectKind: SubjectKind.TEAM,
											roleId: picked,
											orgId,
										},
										{
											onSuccess: () => {
												toast.success("Role granted to team");
												setPicked("");
												setGranting(false);
											},
											onError: (error) =>
												toast.error(
													`Failed to grant role: ${(error as Error).message}`,
												),
										},
									)
								}
							>
								{assignRole.isPending ? "Granting…" : "Grant"}
							</Button>
						</DialogFooter>
					</DialogContent>
				</Dialog>
			)}
		</Section>
	);
}
