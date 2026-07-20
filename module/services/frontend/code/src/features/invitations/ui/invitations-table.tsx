"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Ban } from "lucide-react";
import { useMemo } from "react";
import { toast } from "sonner";
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
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { formatInvitationStatus } from "../model/transforms";
import type { Invitation } from "../model/types";
import { useRevokeInvitation } from "../service/mutations";

const col = createColumnHelper<Invitation>();

export function InvitationsTable({
	invitations,
	isLoading,
}: {
	invitations: Invitation[];
	isLoading: boolean;
}) {
	const revokeInvitation = useRevokeInvitation();

	const columns = useMemo(
		() => [
			col.accessor("email", { header: "Email" }),
			col.accessor("role", {
				header: "Role",
				cell: (info) => (
					<Badge variant="outline" className="capitalize">
						{info.getValue()}
					</Badge>
				),
			}),
			col.accessor("status", {
				header: "Status",
				cell: (info) => {
					const { label, variant } = formatInvitationStatus(info.getValue());
					return <Badge variant={variant}>{label}</Badge>;
				},
			}),
			col.accessor("expiresAt", {
				header: "Expires",
				cell: (info) => (
					<span className="text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("createdAt", {
				header: "Sent",
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
					const inv = row.original;
					// status 1 = PENDING
					if (inv.status !== 1) return null;
					return (
						<AlertDialog>
							<AlertDialogTrigger render={<Button variant="ghost" size="sm" />}>
								<Ban className="h-4 w-4 text-destructive" />
							</AlertDialogTrigger>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Revoke invitation</AlertDialogTitle>
									<AlertDialogDescription>
										This will revoke the invitation sent to {inv.email}. They
										will no longer be able to join the organization with this
										invite.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction
										onClick={() =>
											revokeInvitation.mutate(inv.id, {
												onSuccess: () => toast.success("Invitation revoked"),
												onError: () =>
													toast.error("Failed to revoke invitation"),
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
		[revokeInvitation],
	);

	const table = useReactTable({
		data: invitations,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<DataTable
			table={table}
			isLoading={isLoading}
			emptyMessage="No invitations"
		/>
	);
}
