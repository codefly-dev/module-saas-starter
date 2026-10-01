"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Shield, Trash2, UserPlus, X } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { RoleGate } from "@/components/auth/role-gate";
import { UserPicker } from "@/components/user-picker";
import { InvitationForm } from "@/features/invitations/ui/invitation-form";
import { ManageMemberRolesDialog } from "@/features/roles/ui/manage-member-roles-dialog";
import { useAuth } from "@/lib/auth";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
	Button,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { roleLabel } from "../model/transforms";
import {
	fromOrgRole,
	type OrgMembership,
	type OrgRole,
	toOrgRole,
} from "../model/types";
import { orgMutations } from "../service/mutations";
import { orgQueries } from "../service/queries";

const col = createColumnHelper<OrgMembership>();

const ASSIGNABLE_ROLES: Record<string, string> = {
	member: roleLabel("member"),
	admin: roleLabel("admin"),
	owner: roleLabel("owner"),
};

// A membership change the server refuses on principle — the organization would
// be left with no owner or admin — carries a reason the admin can act on. A
// generic "Failed to remove member" would send them looking for an outage
// instead of at the rule they hit.
//
// Deliberately only FailedPrecondition. Other codes carry wrapped internal
// messages (a quota rejection reads "AddOrgMember: cannot add member: …"), and
// a call path is not something to render to a user.
export function memberErrorMessage(error: unknown, fallback: string): string {
	if (!(error instanceof ConnectError)) return fallback;
	if (error.code !== Code.FailedPrecondition) return fallback;
	return error.rawMessage || fallback;
}

interface OrgMembersPanelProps {
	orgId: string;
	orgName: string;
	onClose?: () => void;
	/**
	 * Where the roster is read from. "membership" is the organization's own
	 * view and requires belonging to it; "platform" is a platform
	 * administrator's view of any organization.
	 */
	source?: "membership" | "platform";
}

export function OrgMembersPanel({
	orgId,
	orgName,
	onClose,
	source = "membership",
}: OrgMembersPanelProps) {
	const queryClient = useQueryClient();
	const { platformRole } = useAuth();
	const [newUserId, setNewUserId] = useState("");
	const [newRole, setNewRole] = useState<OrgRole>("member");

	const membershipRoster = useQuery({
		...orgQueries.members(orgId),
		enabled: source === "membership" && !!orgId,
	});
	const platformRoster = useQuery({
		...orgQueries.roster(orgId),
		enabled: source === "platform" && !!orgId,
	});
	const roster = source === "platform" ? platformRoster : membershipRoster;
	const { data: raw, isLoading, isError, error } = roster;
	const outcome = readOutcome(isError, error);
	// A refused roster takes precedence over rows already on screen. TanStack keeps
	// the last successful answer when a refetch rejects, and `emptyMessage` is only
	// consulted when the table has no rows — so without this, a denial after a
	// successful read leaves the roster rendered and the message unreachable.
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "this organization's members");
	const members: OrgMembership[] = (withheld ? [] : (raw?.members ?? [])).map(
		(m) => ({
			orgId: m.orgId,
			userId: m.userId,
			userEmail: m.userEmail,
			role: toOrgRole(m.role as unknown as number),
			joinedAt: m.joinedAt
				? timestampDate(m.joinedAt).toISOString()
				: undefined,
		}),
	);

	const invalidateRoster = () => {
		queryClient.invalidateQueries({ queryKey: ["org-members", orgId] });
		queryClient.invalidateQueries({ queryKey: ["org-roster", orgId] });
		queryClient.invalidateQueries({ queryKey: ["platform-organizations"] });
	};

	const addMutation = useMutation({
		mutationFn: () =>
			orgMutations.addMember(orgId, newUserId.trim(), fromOrgRole(newRole)),
		onSuccess: () => {
			toast.success("Member added");
			invalidateRoster();
			setNewUserId("");
		},
		onError: (error) =>
			toast.error(memberErrorMessage(error, "Failed to add member")),
	});

	const roleMutation = useMutation({
		mutationFn: ({ userId, role }: { userId: string; role: OrgRole }) =>
			orgMutations.addMember(orgId, userId, fromOrgRole(role)),
		onSuccess: () => {
			toast.success("Role updated");
			invalidateRoster();
		},
		onError: (error) =>
			toast.error(memberErrorMessage(error, "Failed to change role")),
	});

	const removeMutation = useMutation({
		mutationFn: (userId: string) => orgMutations.removeMember(orgId, userId),
		onSuccess: () => {
			toast.success("Member removed");
			invalidateRoster();
		},
		onError: (error) =>
			toast.error(memberErrorMessage(error, "Failed to remove member")),
	});

	const columns = useMemo(
		() => [
			col.accessor("userEmail", {
				header: "User",
				cell: (info) => info.getValue() || "User unavailable",
			}),
			col.accessor("role", {
				header: "Role",
				cell: ({ row }) => {
					const member = row.original;
					const label = member.userEmail || "this member";
					return (
						<Select
							items={ASSIGNABLE_ROLES}
							value={member.role}
							disabled={roleMutation.isPending}
							onValueChange={(next) => {
								if (next && next !== member.role) {
									roleMutation.mutate({
										userId: member.userId,
										role: next as OrgRole,
									});
								}
							}}
						>
							<SelectTrigger
								className="h-8 w-28"
								aria-label={`Role of ${label}`}
							>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="member">Member</SelectItem>
								<SelectItem value="admin">Admin</SelectItem>
								<SelectItem value="owner">Owner</SelectItem>
							</SelectContent>
						</Select>
					);
				},
			}),
			col.accessor("joinedAt", {
				header: "Joined",
				cell: (info) => {
					const v = info.getValue();
					return (
						<span className="text-sm text-muted-foreground">
							{v ? new Date(v).toLocaleDateString() : "-"}
						</span>
					);
				},
			}),
			col.display({
				id: "actions",
				header: "",
				cell: ({ row }) => {
					const label = row.original.userEmail || "this member";
					return (
						<div className="flex items-center justify-end gap-1">
							{/* roles:write hides the Shield from members + admin
                  roles that don't hold roles:write. Server-side authz
                  still gates AssignRole/RevokeRole independently. */}
							<RoleGate requirePermission="roles:write">
								<ManageMemberRolesDialog
									orgId={orgId}
									userId={row.original.userId}
									userLabel={label}
									trigger={
										<Button
											variant="ghost"
											size="sm"
											className="h-8 w-8 p-0"
											aria-label="Manage roles"
										>
											<Shield className="h-4 w-4" />
										</Button>
									}
								/>
							</RoleGate>
							<AlertDialog>
								<AlertDialogTrigger
									render={
										<Button
											variant="ghost"
											size="sm"
											className="h-8 w-8 p-0 text-destructive"
											aria-label={`Remove ${label}`}
										/>
									}
								>
									<Trash2 className="h-4 w-4" />
								</AlertDialogTrigger>
								<AlertDialogContent>
									<AlertDialogHeader>
										<AlertDialogTitle>Remove member</AlertDialogTitle>
										<AlertDialogDescription>
											{label} will lose access to {orgName} and its teams, and
											is signed out of it.
										</AlertDialogDescription>
									</AlertDialogHeader>
									<AlertDialogFooter>
										<AlertDialogCancel>Cancel</AlertDialogCancel>
										<AlertDialogAction
											onClick={() => removeMutation.mutate(row.original.userId)}
										>
											Remove
										</AlertDialogAction>
									</AlertDialogFooter>
								</AlertDialogContent>
							</AlertDialog>
						</div>
					);
				},
			}),
		],
		[removeMutation, roleMutation, orgId, orgName],
	);

	const table = useReactTable({
		data: members,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between">
				<h3 className="text-lg font-semibold">
					Members of <span className="text-muted-foreground">{orgName}</span>
				</h3>
				{onClose && (
					<Button
						variant="ghost"
						size="sm"
						onClick={onClose}
						aria-label="Close"
					>
						<X className="h-4 w-4" />
					</Button>
				)}
			</div>

			{platformRole ? (
				<div className="flex items-center gap-2">
					<UserPicker
						value={newUserId}
						onChange={setNewUserId}
						exclude={members.map((member) => member.userId)}
					/>
					<Select
						items={ASSIGNABLE_ROLES}
						value={newRole}
						onValueChange={(v) => {
							if (v) setNewRole(v as OrgRole);
						}}
					>
						<SelectTrigger
							className="w-32"
							aria-label="Role for the new member"
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="member">Member</SelectItem>
							<SelectItem value="admin">Admin</SelectItem>
							<SelectItem value="owner">Owner</SelectItem>
						</SelectContent>
					</Select>
					<Button
						size="sm"
						disabled={addMutation.isPending || !newUserId.trim()}
						onClick={() => addMutation.mutate()}
					>
						<UserPlus className="mr-2 h-4 w-4" />
						{addMutation.isPending ? "Adding..." : "Add"}
					</Button>
				</div>
			) : (
				// An organization administrator does not search the platform's user
				// directory; they invite by email, and get a link to share when the
				// deployment sends no email.
				<InvitationForm orgId={orgId} />
			)}

			{stale && (
				<p role="status" className="text-sm text-muted-foreground">
					{stale}
				</p>
			)}
			{/* An unread `isError` rendered a failed or denied roster read as "No
			    members in this organization." — the one sentence an administrator
			    would act on, about a tenant whose roster was never read. */}
			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage={readOutcomeMessage(
					outcome,
					"this organization's members",
					"No members in this organization.",
				)}
			/>
		</div>
	);
}
