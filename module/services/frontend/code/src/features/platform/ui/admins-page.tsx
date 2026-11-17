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

import { refusalToast } from "@/features/auth/model/step-up";
import { UserLabel } from "@/components/user-label";
import { UserPicker } from "@/components/user-picker";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
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
	ErrorState,
	Label,
	Page as PageBody,
	PageHeader,
	Panel,
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

// The rows when there is no answer. TanStack Table resets the page on every new
// `data` array and that reset re-renders this page, so a fresh `[]` per render
// would re-render it forever while the read is refused or disabled.
const NO_ADMINS: PlatformAdmin[] = [];

const platformRoleLabels: Record<string, string> = {
	super_admin: "Super admin",
	billing: "Billing",
	support: "Support",
};

export function AdminsPage() {
	const {
		data: admins = NO_ADMINS,
		isLoading,
		isError,
		error,
	} = usePlatformAdmins();
	// A failed read defaults the rows to none, exactly as a platform with no
	// administrators does; the table is replaced so the two cannot be confused.
	const outcome = readOutcome(isError, error);
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
				onError: (error) => refusalToast(error, "Couldn't grant the role"),
			},
		);
	}

	const columns = useMemo(
		() => [
			col.accessor("userId", {
				header: "User",
				cell: (info) => <UserLabel userId={info.getValue()} />,
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
					return (
						<Badge variant={variant}>{platformRoleLabels[r] ?? r}</Badge>
					);
				},
			}),
			col.accessor("grantedBy", {
				header: "Granted by",
				cell: (info) => (
					<UserLabel
						userId={info.getValue()}
						fallback={info.getValue() ? "User unavailable" : "System"}
					/>
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
										This will revoke the &quot;
										{platformRoleLabels[admin.platformRole] ??
											admin.platformRole}
										&quot; role
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
												onError: (error) =>
													refusalToast(error, "Couldn't revoke the role"),
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
		<PageBody>
			<PageHeader
				title="Platform admins"
				description="Grant and revoke platform-level admin roles."
				actions={
					<Dialog open={open} onOpenChange={setOpen}>
						<DialogTrigger render={<Button />}>
							<Plus className="mr-2 h-4 w-4" />
							Grant role
						</DialogTrigger>
						<DialogContent className="sm:max-w-md">
							<DialogHeader>
								<DialogTitle>Grant platform role</DialogTitle>
								<DialogDescription>
									Assign a platform-level role to a user.
								</DialogDescription>
							</DialogHeader>
							<div className="space-y-4 py-4">
								<div className="space-y-2">
									<UserPicker value={userId} onChange={setUserId} />
								</div>
								<div className="space-y-2">
									<Label>Role</Label>
									<Select
										value={role}
										items={PLATFORM_ROLES.map((value) => ({
											value,
											label: platformRoleLabels[value] ?? value,
										}))}
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
													{platformRoleLabels[r] ?? r}
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
				}
			/>

			{isError ? (
				<Panel>
					<ErrorState
						title="Couldn't load the platform admins"
						detail={
							outcome === "forbidden"
								? "Your account doesn't have the platform role this list needs."
								: "Reload the page to try again. If it keeps failing, tell your administrator."
						}
					/>
				</Panel>
			) : (
				<DataTable
					table={table}
					isLoading={isLoading}
					emptyMessage="No platform admins"
				/>
			)}
		</PageBody>
	);
}
