"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
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
	Badge,
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { PlatformAdmin } from "../model/types";
import { PLATFORM_ROLES } from "../model/types";
import {
	useGrantPlatformRole,
	useRevokePlatformRole,
} from "../service/mutations";
import { usePlatformAdmins } from "../service/queries";

const col = createColumnHelper<PlatformAdmin>();

export function AdminsPage() {
	const { data: admins = [], isLoading } = usePlatformAdmins();
	const grantRole = useGrantPlatformRole();
	const revokeRole = useRevokePlatformRole();

	const [open, setOpen] = useState(false);
	const [userId, setUserId] = useState("");
	const [role, setRole] = useState<string>(PLATFORM_ROLES[0]);

	function handleGrant() {
		if (!userId.trim()) return;
		grantRole.mutate(
			{ userId: userId.trim(), platformRole: role },
			{
				onSuccess: () => {
					toast.success("Platform role granted");
					setUserId("");
					setOpen(false);
				},
				onError: () => toast.error("Failed to grant role"),
			},
		);
	}

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
			col.accessor("platformRole", {
				header: "Role",
				cell: (info) => {
					const r = info.getValue();
					const variant =
						r === "super_admin"
							? "destructive"
							: r === "billing"
								? "default"
								: "secondary";
					return <Badge variant={variant}>{r}</Badge>;
				},
			}),
			col.accessor("grantedBy", {
				header: "Granted By",
				cell: (info) => (
					<span className="font-mono text-xs text-muted-foreground">
						{truncateUUID(info.getValue())}
					</span>
				),
			}),
			col.accessor("grantedAt", {
				header: "Granted",
				cell: (info) => (
					<span className="text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.display({
				id: "actions",
				header: "",
				cell: ({ row }) => {
					const admin = row.original;
					return (
						<AlertDialog>
							<AlertDialogTrigger render={<Button variant="ghost" size="sm" />}>
								<Trash2 className="h-4 w-4 text-destructive" />
							</AlertDialogTrigger>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Revoke platform role</AlertDialogTitle>
									<AlertDialogDescription>
										This will revoke the &quot;{admin.platformRole}&quot; role
										from this user. They will lose all associated platform
										permissions.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction
										onClick={() =>
											revokeRole.mutate(admin.userId, {
												onSuccess: () => toast.success("Role revoked"),
												onError: () => toast.error("Failed to revoke role"),
											})
										}
									>
										Revoke
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					);
				},
			}),
		],
		[revokeRole],
	);

	const table = useReactTable({
		data: admins as PlatformAdmin[],
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Platform Admins</h1>
					<p className="text-muted-foreground">
						Grant and revoke platform-level admin roles.
					</p>
				</div>

				<Dialog open={open} onOpenChange={setOpen}>
					<DialogTrigger render={<Button />}>
						<Plus className="mr-2 h-4 w-4" />
						Grant Role
					</DialogTrigger>
					<DialogContent className="sm:max-w-md">
						<DialogHeader>
							<DialogTitle>Grant Platform Role</DialogTitle>
							<DialogDescription>
								Assign a platform-level role to a user.
							</DialogDescription>
						</DialogHeader>
						<div className="space-y-4 py-4">
							<div className="space-y-2">
								<Label htmlFor="admin-user">User ID</Label>
								<Input
									id="admin-user"
									placeholder="User UUID"
									value={userId}
									onChange={(e) => setUserId(e.target.value)}
								/>
							</div>
							<div className="space-y-2">
								<Label>Role</Label>
								<Select
									value={role}
									onValueChange={(v) => {
										if (v) setRole(v);
									}}
								>
									<SelectTrigger>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{PLATFORM_ROLES.map((r) => (
											<SelectItem key={r} value={r}>
												{r}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
						</div>
						<DialogFooter>
							<Button variant="outline" onClick={() => setOpen(false)}>
								Cancel
							</Button>
							<Button
								onClick={handleGrant}
								disabled={grantRole.isPending || !userId.trim()}
							>
								{grantRole.isPending ? "Granting..." : "Grant"}
							</Button>
						</DialogFooter>
					</DialogContent>
				</Dialog>
			</div>

			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No platform admins"
			/>
		</div>
	);
}
