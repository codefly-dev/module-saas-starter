"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Plus, Trash2 } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { UserLabel } from "@/components/user-label";
import { UserPicker } from "@/components/user-picker";
import { refusalMessage, refusalToast } from "@/features/auth/model/step-up";
import { useAuth } from "@/lib/auth";
import { isSuperAdmin } from "@/lib/permissions";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	AlertDialog,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
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
	const { platformRole } = useAuth();
	const canManage = isSuperAdmin(platformRole);
	const busy = useRef(false);
	const [revokeTarget, setRevokeTarget] = useState<PlatformAdmin | null>(null);
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
	const [role, setRole] = useState<string>("");

	function handleGrant() {
		if (
			!canManage ||
			busy.current ||
			!userId.trim() ||
			!PLATFORM_ROLES.some((value) => value === role)
		)
			return;
		busy.current = true;
		grantRole.mutate(
			{ userId: userId.trim(), platformRole: role },
			{
				onSuccess: () => {
					toast.success("Platform role granted");
					setUserId("");
					setRole("");
					setOpen(false);
				},
				onError: (error) => refusalToast(error, "Couldn't grant the role"),
				onSettled: () => {
					busy.current = false;
				},
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
						<Badge variant={variant}>
							{platformRoleLabels[r] ?? "Custom role"}
						</Badge>
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
				cell: ({ row }) =>
					canManage ? (
						<Button
							variant="ghost"
							size="sm"
							onClick={() => setRevokeTarget(row.original)}
						>
							<Trash2 aria-hidden="true" className="h-4 w-4 text-destructive" />
							<span className="sr-only">
								Remove access for <UserLabel userId={row.original.userId} />
							</span>
						</Button>
					) : null,
			}),
		],
		[canManage],
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
				description="Manage who can administer all organizations."
				actions={
					<Dialog
						open={open}
						onOpenChange={(next) => {
							if (busy.current || grantRole.isPending) return;
							grantRole.reset();
							setOpen(next);
						}}
					>
						{canManage && (
							<DialogTrigger render={<Button />}>
								<Plus className="mr-2 h-4 w-4" />
								Grant role
							</DialogTrigger>
						)}
						<DialogContent className="sm:max-w-md">
							<DialogHeader>
								<DialogTitle>Grant platform role</DialogTitle>
								<DialogDescription>
									Assign a platform-level role to a user.
								</DialogDescription>
							</DialogHeader>
							<div className="space-y-4 py-4">
								<div className="space-y-2">
									<UserPicker
										value={userId}
										onChange={setUserId}
										disabled={grantRole.isPending}
									/>
								</div>
								<Field label="Role">
									{(control) => (
										<Select
											value={role}
											disabled={grantRole.isPending}
											items={PLATFORM_ROLES.map((value) => ({
												value,
												label: platformRoleLabels[value] ?? value,
											}))}
											onValueChange={(v) => {
												if (v) setRole(v);
											}}
										>
											<SelectTrigger {...control}>
												<SelectValue placeholder="Choose a role" />
											</SelectTrigger>
											<SelectContent>
												{PLATFORM_ROLES.map((r) => (
													<SelectItem key={r} value={r}>
														{platformRoleLabels[r] ?? "Custom role"}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									)}
								</Field>
							</div>
							{grantRole.isError && (
								<ErrorState
									title="Couldn't grant the role"
									detail={refusalMessage(
										grantRole.error,
										"Your choice has been kept. Try saving it again.",
									)}
								/>
							)}
							<DialogFooter>
								<Button
									variant="outline"
									disabled={grantRole.isPending}
									onClick={() => setOpen(false)}
								>
									Cancel
								</Button>
								<Button
									onClick={handleGrant}
									disabled={
										grantRole.isPending || !userId.trim() || !role || !canManage
									}
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
								? "You don't have access to this list. Ask a platform administrator to grant access."
								: "Reload the page to try again. If it keeps failing, tell your administrator."
						}
					/>
				</Panel>
			) : (
				<DataTable
					table={table}
					isLoading={isLoading}
					emptyMessage="No platform administrators yet. Grant a role to give someone access."
				/>
			)}
			<AlertDialog
				open={!!revokeTarget}
				onOpenChange={(next) => {
					if (busy.current || revokeRole.isPending) return;
					if (!next) {
						setRevokeTarget(null);
						revokeRole.reset();
					}
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Remove platform access?</AlertDialogTitle>
						<AlertDialogDescription>
							{revokeTarget && <UserLabel userId={revokeTarget.userId} />} will
							lose their{" "}
							{revokeTarget &&
								(platformRoleLabels[revokeTarget.platformRole] ??
									"custom")}{" "}
							role and its access across organizations.
						</AlertDialogDescription>
					</AlertDialogHeader>
					{revokeRole.isError && (
						<ErrorState
							title="Couldn't remove access"
							detail={refusalMessage(
								revokeRole.error,
								"Access has not been removed. Try again.",
							)}
						/>
					)}
					<AlertDialogFooter>
						<AlertDialogCancel disabled={revokeRole.isPending}>
							Cancel
						</AlertDialogCancel>
						<Button
							variant="destructive"
							disabled={revokeRole.isPending || !canManage}
							onClick={() => {
								if (!canManage || !revokeTarget || busy.current) return;
								busy.current = true;
								revokeRole.mutate(revokeTarget.userId, {
									onSuccess: () => {
										setRevokeTarget(null);
										toast.success("Platform access removed");
									},
									onError: (error) =>
										refusalToast(error, "Couldn't remove access"),
									onSettled: () => {
										busy.current = false;
									},
								});
							}}
						>
							{revokeRole.isPending ? "Removing…" : "Remove access"}
						</Button>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</PageBody>
	);
}
