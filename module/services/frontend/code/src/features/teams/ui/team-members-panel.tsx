"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Trash2, UserPlus, X } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Input,
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

interface TeamMembersPanelProps {
	teamId: string;
	teamName: string;
	onClose: () => void;
}

export function TeamMembersPanel({
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

	const addMutation = useMutation({
		mutationFn: () =>
			teamMutations.addMember(teamId, newUserId.trim(), fromTeamRole(newRole)),
		onSuccess: () => {
			toast.success("Member added");
			queryClient.invalidateQueries({ queryKey: ["team-members", teamId] });
			setNewUserId("");
		},
		onError: () => toast.error("Failed to add member"),
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
				<Input
					placeholder="User ID to add..."
					value={newUserId}
					onChange={(e) => setNewUserId(e.target.value)}
					onKeyDown={(e) =>
						e.key === "Enter" && newUserId.trim() && addMutation.mutate()
					}
					className="max-w-xs"
				/>
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
					disabled={addMutation.isPending || !newUserId.trim()}
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
