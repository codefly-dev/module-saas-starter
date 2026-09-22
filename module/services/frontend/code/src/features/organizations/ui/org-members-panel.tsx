"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Shield, Trash2, UserPlus, X } from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { RoleGate } from "@/components/auth/role-gate";
import { UserPicker } from "@/components/user-picker";
import { ManageMemberRolesDialog } from "@/features/roles/ui/manage-member-roles-dialog";
import { useAuth } from "@/lib/auth";
import {
	Badge,
	Button,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { getRoleBadgeVariant, roleLabel } from "../model/transforms";
import { fromOrgRole, type OrgMembership, toOrgRole } from "../model/types";
import { orgMutations } from "../service/mutations";
import { orgQueries } from "../service/queries";

const col = createColumnHelper<OrgMembership>();

interface OrgMembersPanelProps {
	orgId: string;
	orgName: string;
	onClose: () => void;
}

export function OrgMembersPanel({
	orgId,
	orgName,
	onClose,
}: OrgMembersPanelProps) {
	const queryClient = useQueryClient();
	const { platformRole } = useAuth();
	const [newUserId, setNewUserId] = useState("");
	const [newRole, setNewRole] = useState<"member" | "admin">("member");

	const { data: raw, isLoading } = useQuery(orgQueries.members(orgId));
	const members: OrgMembership[] = (raw?.members ?? []).map((m) => ({
		orgId: m.orgId,
		userId: m.userId,
		userEmail: m.userEmail,
		role: toOrgRole(m.role as unknown as number),
		joinedAt: m.joinedAt ? timestampDate(m.joinedAt).toISOString() : undefined,
	}));

	const addMutation = useMutation({
		mutationFn: () =>
			orgMutations.addMember(orgId, newUserId.trim(), fromOrgRole(newRole)),
		onSuccess: () => {
			toast.success("Member added");
			queryClient.invalidateQueries({ queryKey: ["org-members", orgId] });
			setNewUserId("");
		},
		onError: () => toast.error("Failed to add member"),
	});

	const removeMutation = useMutation({
		mutationFn: (userId: string) => orgMutations.removeMember(orgId, userId),
		onSuccess: () => {
			toast.success("Member removed");
			queryClient.invalidateQueries({ queryKey: ["org-members", orgId] });
		},
		onError: () => toast.error("Failed to remove member"),
	});

	const columns = useMemo(
		() => [
			col.accessor("userEmail", {
				header: "User",
				cell: (info) => info.getValue() || "User unavailable",
			}),
			col.accessor("role", {
				header: "Role",
				cell: (info) => {
					const r = info.getValue();
					return <Badge variant={getRoleBadgeVariant(r)}>{roleLabel(r)}</Badge>;
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
				cell: ({ row }) => (
					<div className="flex items-center justify-end gap-1">
						{/* roles:write hides the Shield from members + admin
                roles that don't hold roles:write. Server-side authz
                still gates AssignRole/RevokeRole independently. */}
						<RoleGate requirePermission="roles:write">
							<ManageMemberRolesDialog
								orgId={orgId}
								userId={row.original.userId}
								userLabel={row.original.userEmail || "this member"}
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
						<Button
							variant="ghost"
							size="sm"
							className="h-8 w-8 p-0 text-destructive"
							onClick={() => removeMutation.mutate(row.original.userId)}
							aria-label="Remove member"
						>
							<Trash2 className="h-4 w-4" />
						</Button>
					</div>
				),
			}),
		],
		[removeMutation, orgId],
	);

	const table = useReactTable({
		data: members,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});

	return (
		<div className="mt-8 space-y-4">
			<div className="flex items-center justify-between">
				<h3 className="text-lg font-semibold">
					Members of <span className="text-muted-foreground">{orgName}</span>
				</h3>
				<Button variant="ghost" size="sm" onClick={onClose}>
					<X className="h-4 w-4" />
				</Button>
			</div>

			{platformRole ? (
				<div className="flex items-center gap-2">
					<UserPicker
						value={newUserId}
						onChange={setNewUserId}
						exclude={members.map((member) => member.userId)}
					/>
					<Select
						items={{ member: "Member", admin: "Admin" }}
						value={newRole}
						onValueChange={(v) => setNewRole(v as "member" | "admin")}
					>
						<SelectTrigger className="w-32">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="member">Member</SelectItem>
							<SelectItem value="admin">Admin</SelectItem>
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
				<Link href="/admin/invitations" className="text-sm underline">
					Invite a member by email
				</Link>
			)}

			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No members in this organization."
			/>
		</div>
	);
}
