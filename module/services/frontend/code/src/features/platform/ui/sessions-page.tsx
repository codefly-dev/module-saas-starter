"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { LogOut, MoreHorizontal } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { UserLabel } from "@/components/user-label";
import { failureText } from "@/shared/lib/failure-text";
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
	Badge,
	Button,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Stack,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { SessionInfo } from "../model/types";
import { useRevokeSession } from "../service/mutations";
import { useActiveSessions } from "../service/queries";

const col = createColumnHelper<SessionInfo>();

// The rows when there is no answer. TanStack Table resets the page on every new
// `data` array and that reset re-renders this page, so a fresh `[]` per render
// would re-render it forever while the read is refused or disabled.
const NO_SESSIONS: SessionInfo[] = [];

export function SessionsPage() {
	const {
		data: sessions = NO_SESSIONS,
		isLoading,
		isError,
		error,
		refetch,
	} = useActiveSessions();
	const [revokeTarget, setRevokeTarget] = useState<SessionInfo | null>(null);
	const revoke = useRevokeSession();

	const columns = useMemo(
		() => [
			col.accessor("userId", {
				header: "User",
				cell: (info) => <UserLabel userId={info.getValue()} />,
			}),
			col.accessor("actingAsUserId", {
				header: "Type",
				cell: (info) => {
					const target = info.getValue();
					if (!target) {
						return <span className="text-muted-foreground">Login</span>;
					}
					return (
						<Badge variant="outline" className="text-xs">
							Impersonating <UserLabel userId={target} />
						</Badge>
					);
				},
			}),
			col.accessor("ipAddress", {
				header: "IP address",
				cell: (info) => (
					<span className="font-mono text-xs">{info.getValue() || "-"}</span>
				),
			}),
			col.accessor("deviceInfo", {
				header: "Device",
				cell: (info) => {
					const device = info.getValue();
					if (!device || Object.keys(device).length === 0) {
						return <span className="text-muted-foreground">-</span>;
					}
					return (
						<span className="text-xs text-muted-foreground">
							{device.browser || device.os || Object.values(device)[0] || "-"}
						</span>
					);
				},
			}),
			col.accessor("lastActiveAt", {
				header: "Last active",
				cell: (info) => (
					<span className="text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("idleExpiresAt", {
				header: "Idle expiry",
				cell: (info) => (
					<span className="text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("expiresAt", {
				header: "Absolute expiry",
				cell: (info) => (
					<span className="text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.display({
				id: "actions",
				cell: ({ row }) => (
					<DropdownMenu>
						<DropdownMenuTrigger
							render={
								<Button variant="ghost" size="sm" className="h-8 w-8 p-0" />
							}
						>
							<MoreHorizontal className="h-4 w-4" />
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end">
							<DropdownMenuGroup>
								<DropdownMenuLabel>Actions</DropdownMenuLabel>
								<DropdownMenuSeparator />
								<DropdownMenuItem
									onClick={() => setRevokeTarget(row.original)}
									className="text-destructive focus:text-destructive"
								>
									<LogOut className="mr-2 h-4 w-4" />
									Force logout
								</DropdownMenuItem>
							</DropdownMenuGroup>
						</DropdownMenuContent>
					</DropdownMenu>
				),
			}),
		],
		[],
	);

	const table = useReactTable({
		data: sessions as SessionInfo[],
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<PageBody>
			<PageHeader
				title="Active sessions"
				description="View and monitor active user sessions."
			/>

			{isError ? (
				<Panel>
					<Stack direction="row" gap={4} align="center" justify="between">
						<ErrorState
							title="Couldn't load the active sessions"
							detail={
								readOutcome(isError, error) === "forbidden"
									? "Your account doesn't have the platform role this list needs."
									: "If it keeps failing, tell your administrator."
							}
						/>
						<Button variant="outline" onClick={() => void refetch()}>
							Retry
						</Button>
					</Stack>
				</Panel>
			) : (
				<DataTable
					table={table}
					isLoading={isLoading}
					emptyMessage="No active sessions"
				/>
			)}

			{revokeTarget && (
				<AlertDialog open onOpenChange={(o) => !o && setRevokeTarget(null)}>
					<AlertDialogContent>
						<AlertDialogHeader>
							<AlertDialogTitle>Force logout?</AlertDialogTitle>
							<AlertDialogDescription>
								This revokes the session for user{" "}
								<UserLabel userId={revokeTarget.userId} />. They&apos;ll be
								signed out on that device immediately.
							</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter>
							<AlertDialogCancel onClick={() => setRevokeTarget(null)}>
								Cancel
							</AlertDialogCancel>
							<AlertDialogAction
								onClick={() =>
									revoke.mutate(
										{ sessionId: revokeTarget.id, reason: "revoked_by_admin" },
										{
											onSuccess: () => {
												toast.success("Session revoked");
												setRevokeTarget(null);
											},
											onError: (failure) =>
												toast.error(
													failureText("sign this session out", failure),
												),
										},
									)
								}
								disabled={revoke.isPending}
								className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
							>
								{revoke.isPending ? "Revoking..." : "Force logout"}
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
			)}
		</PageBody>
	);
}
