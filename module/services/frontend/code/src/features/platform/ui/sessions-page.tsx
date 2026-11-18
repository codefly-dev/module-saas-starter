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
import { sessionDevice } from "../model/session-device";
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
				cell: (info) => (
					<span className="text-sm">{sessionDevice(info.getValue())}</span>
				),
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
				header: "Signs out if inactive",
				cell: (info) => (
					<span className="text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("expiresAt", {
				header: "Session ends",
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
								<Button
									aria-label="Session actions"
									variant="ghost"
									size="icon-sm"
								/>
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
									? "You don't have access to active sessions. Ask a platform administrator to grant access."
									: "If it keeps failing, tell your administrator."
							}
						/>
						{readOutcome(isError, error) !== "forbidden" && (
							<Button variant="outline" onClick={() => void refetch()}>
								Try again
							</Button>
						)}
					</Stack>
				</Panel>
			) : (
				<DataTable
					table={table}
					isLoading={isLoading}
					emptyMessage="No one is signed in. Sessions appear here when someone signs in."
				/>
			)}

			{revokeTarget && (
				<AlertDialog
					open
					onOpenChange={(o) => !o && !revoke.isPending && setRevokeTarget(null)}
				>
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
							<AlertDialogCancel
								disabled={revoke.isPending}
								onClick={() => setRevokeTarget(null)}
							>
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
