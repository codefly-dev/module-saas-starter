"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Trash2, UserPlus, X } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { orgQueries } from "@/features/organizations/service/queries";
import { truncateUUID } from "@/shared/lib/utils";
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
import { fromTeamRole, type TeamMembership, toTeamRole } from "../model/types";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";

const col = createColumnHelper<TeamMembership>();

// Only an ineligible target carries a message written for whoever is reading it.
// Handlers return every other failure unwrapped, so its text is server internals
// — reporting it verbatim would put that in a toast.
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

interface TeamMembersPanelProps {
	orgId: string;
	teamId: string;
	teamName: string;
	onClose: () => void;
}

export function TeamMembersPanel({
	orgId,
	teamId,
	teamName,
	onClose,
}: TeamMembersPanelProps) {
	const queryClient = useQueryClient();
	const [newUserId, setNewUserId] = useState("");
	const [newRole, setNewRole] = useState<"member" | "admin">("member");

	const { data: raw, isLoading } = useQuery(teamQueries.members(teamId));
	const members: TeamMembership[] = (raw?.members ?? []).map((m) => ({
		teamId: m.teamId,
		userId: m.userId,
		role: toTeamRole(m.role as unknown as number),
		joinedAt: m.joinedAt ? timestampDate(m.joinedAt).toISOString() : undefined,
	}));

	// A team membership only exists under a membership of the team's own
	// organization, so the picker offers exactly those. The server enforces it
	// regardless of what this list happens to hold.
	const { data: orgMemberData } = useQuery(orgQueries.members(orgId));
	const alreadyOnTeam = new Set(members.map((m) => m.userId));
	const eligible = (orgMemberData?.members ?? []).filter(
		(m) => !alreadyOnTeam.has(m.userId),
	);

	const addMutation = useMutation({
		mutationFn: () =>
			teamMutations.addMember(teamId, newUserId, fromTeamRole(newRole)),
		onSuccess: () => {
			toast.success("Member added");
			queryClient.invalidateQueries({ queryKey: ["team-members", teamId] });
			setNewUserId("");
		},
		onError: (error) => toast.error(addMemberErrorMessage(error)),
	});

	const removeMutation = useMutation({
		mutationFn: (userId: string) => teamMutations.removeMember(teamId, userId),
		onSuccess: () => {
			toast.success("Member removed");
			queryClient.invalidateQueries({ queryKey: ["team-members", teamId] });
		},
		onError: () => toast.error("Failed to remove member"),
	});

	const columns = useMemo(
		() => [
			col.accessor("userId", {
				header: "User ID",
				cell: (info) => (
					<span className="font-mono text-xs">
						{truncateUUID(info.getValue())}
					</span>
				),
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
				cell: ({ row }) => (
					<Button
						variant="ghost"
						size="sm"
						className="h-8 w-8 p-0 text-destructive"
						onClick={() => removeMutation.mutate(row.original.userId)}
					>
						<Trash2 className="h-4 w-4" />
					</Button>
				),
			}),
		],
		[removeMutation],
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
					Members of <span className="text-muted-foreground">{teamName}</span>
				</h3>
				<Button variant="ghost" size="sm" onClick={onClose}>
					<X className="h-4 w-4" />
				</Button>
			</div>

			<div className="flex items-center gap-2">
				<Select
					value={newUserId}
					onValueChange={(v) => setNewUserId(v ?? "")}
					disabled={eligible.length === 0}
				>
					<SelectTrigger className="w-64">
						<SelectValue
							placeholder={
								eligible.length === 0
									? "Every organization member is on this team"
									: "Organization member to add..."
							}
						/>
					</SelectTrigger>
					<SelectContent>
						{eligible.map((m) => (
							<SelectItem key={m.userId} value={m.userId}>
								{truncateUUID(m.userId)}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<Select
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
					disabled={addMutation.isPending || !newUserId}
					onClick={() => addMutation.mutate()}
				>
					<UserPlus className="mr-2 h-4 w-4" />
					{addMutation.isPending ? "Adding..." : "Add"}
				</Button>
			</div>

			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No members in this team."
			/>
		</div>
	);
}
