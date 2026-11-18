"use client";

import { Plus } from "lucide-react";
import Link from "next/link";
import { useRef, useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Badge,
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
	ErrorState,
	Field,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Spinner,
	useLoadingPhase,
} from "@/shared/ui";
import { useAssignRole } from "../service/mutations";
import { useRoleAssignments, useRoles } from "../service/queries";
import { RevokeRoleButton } from "./revoke-role-button";

interface ManageMemberRolesDialogProps {
	orgId: string;
	userId: string;
	userLabel: string;
	trigger?: React.ReactElement;
}

// ManageMemberRolesDialog — the primary surface for assigning/
// revoking custom roles for a user within an org. Wraps the
// previously-orphan useAssignRole / useRevokeRole hooks.
//
// Visible roles = built-ins (org_id IS NULL) + this org's custom
// roles, courtesy of the polymorphic RLS policy on `roles` (Phase 2E).
// Assignments shown are filtered by subjectId so the picker shows
// only this user's grants in this organization.
export function ManageMemberRolesDialog({
	orgId,
	userId,
	userLabel,
	trigger,
}: ManageMemberRolesDialogProps) {
	const [open, setOpen] = useState(false);
	const [picked, setPicked] = useState<string>("");
	const pending = useRef(false);

	const {
		data: roles = [],
		isError: rolesFailed,
		isPending: rolesLoading,
		error: rolesError,
	} = useRoles(orgId);
	const {
		data: assignments = [],
		isLoading,
		isError,
		error,
	} = useRoleAssignments(orgId, userId);
	const { indicator, quiet } = useLoadingPhase(isLoading);
	const outcome = readOutcome(isError, error);
	// A refused read of this member's grants withholds the badges rather than
	// letting a retained answer stand in for one this reader may no longer see.
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "this member's roles");
	// The picker offers what the role list actually returned. A failed or refused
	// list is not an organization with no roles in it, and "No roles available"
	// beside a working Create-a-role link would send the reader to make a
	// duplicate of one they simply could not read.
	const rolesOutcome = readOutcome(rolesFailed, rolesError);

	const assignRole = useAssignRole();

	// Roles already granted to this user — disable in the picker so
	// users can't double-assign (the backend's ON CONFLICT DO NOTHING
	// would silently swallow it; better to surface in the UI).
	const grantedIds = new Set(
		(withheld ? [] : assignments).map((a) => a.roleId),
	);
	const availableRoles = roles.filter((r) => !grantedIds.has(r.id));

	const roleNameById = new Map(roles.map((r) => [r.id, r.name] as const));

	function handleAssign() {
		if (
			!picked ||
			pending.current ||
			rolesFailed ||
			isError ||
			rolesLoading ||
			isLoading
		)
			return;
		pending.current = true;
		assignRole.mutate(
			{ subjectId: userId, roleId: picked, orgId },
			{
				onSuccess: () => {
					toast.success(`Role assigned to ${userLabel}`);
					setPicked("");
				},
				onSettled: () => {
					pending.current = false;
				},
			},
		);
	}

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!pending.current) setOpen(next);
			}}
		>
			<DialogTrigger render={trigger ?? <Button variant="outline" size="sm" />}>
				{!trigger && "Manage roles"}
			</DialogTrigger>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Manage roles</DialogTitle>
					<DialogDescription>
						Roles assigned to{" "}
						<span className="font-mono text-xs">{userLabel}</span> in this
						organization.
					</DialogDescription>
				</DialogHeader>

				<div className="space-y-4 py-2">
					<div className="space-y-2">
						<div className="text-sm font-medium">Current assignments</div>
						{quiet ? null : indicator ? (
							<Spinner label="Loading this member's roles" size="sm" />
						) : withheld || assignments.length === 0 ? (
							<div
								className="text-sm text-muted-foreground"
								{...(isError ? { role: "alert" as const } : {})}
							>
								{readOutcomeMessage(
									outcome,
									"this member's roles",
									"No custom roles assigned.",
								)}
							</div>
						) : (
							<div className="space-y-2">
								{stale && (
									<div role="status" className="text-sm text-muted-foreground">
										{stale}
									</div>
								)}
								<div className="flex flex-wrap gap-1">
									{assignments.map((a) => (
										<Badge
											key={a.id}
											variant="secondary"
											className="font-mono text-xs gap-1"
										>
											{roleNameById.get(a.roleId) ?? "Role unavailable"}
											<RevokeRoleButton
												subjectId={userId}
												subjectLabel={userLabel}
												roleId={a.roleId}
												roleName={roleNameById.get(a.roleId) || "this role"}
												orgId={orgId}
												scope={a.scope}
											/>
										</Badge>
									))}
								</div>
							</div>
						)}
					</div>

					<div className="space-y-2">
						<Field
							label="Grant a role"
							description={
								rolesLoading ? (
									<Spinner label="Loading available roles" size="sm" />
								) : undefined
							}
							error={
								assignRole.isError
									? failureText("assign this role", assignRole.error)
									: undefined
							}
						>
							{(control) => (
								<div className="flex items-center gap-2">
									<Select
										disabled={
											assignRole.isPending ||
											rolesLoading ||
											rolesFailed ||
											isError ||
											isLoading
										}
										value={picked}
										onValueChange={(v) => {
											setPicked(v ?? "");
											assignRole.reset();
										}}
									>
										<SelectTrigger {...control} className="flex-1">
											<SelectValue placeholder="Pick a role…">
												{picked
													? roleNameById.get(picked) || "Role unavailable"
													: undefined}
											</SelectValue>
										</SelectTrigger>
										<SelectContent>
											{availableRoles.length === 0 ? (
												<div className="px-2 py-2 text-sm text-muted-foreground">
													<div>
														{readOutcomeMessage(
															rolesOutcome,
															"this organization's roles",
															"No roles available.",
														)}
													</div>
													{!rolesFailed && !rolesLoading && (
														<Link
															href="/admin/roles"
															className="text-primary hover:underline"
															onClick={() => setOpen(false)}
														>
															Create a custom role →
														</Link>
													)}
												</div>
											) : (
												availableRoles.map((r) => (
													<SelectItem key={r.id} value={r.id}>
														<span className="flex items-center gap-2">
															{r.name}
															{r.builtIn ? (
																<Badge
																	variant="outline"
																	className="text-[10px]"
																>
																	built-in
																</Badge>
															) : (
																<Badge
																	variant="secondary"
																	className="text-[10px]"
																>
																	custom
																</Badge>
															)}
														</span>
													</SelectItem>
												))
											)}
										</SelectContent>
									</Select>
									<Button
										size="sm"
										onClick={handleAssign}
										disabled={
											!picked ||
											assignRole.isPending ||
											rolesFailed ||
											isError ||
											rolesLoading ||
											isLoading
										}
									>
										<Plus className="mr-1 h-4 w-4" />
										{assignRole.isPending ? "Assigning…" : "Assign"}
									</Button>
								</div>
							)}
						</Field>
						{rolesFailed && (
							<ErrorState
								title="Roles unavailable"
								detail={readOutcomeMessage(
									rolesOutcome,
									"this organization's roles",
									"",
								)}
							/>
						)}
					</div>
				</div>

				<DialogFooter>
					<Button
						variant="outline"
						disabled={assignRole.isPending}
						onClick={() => setOpen(false)}
					>
						Done
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
