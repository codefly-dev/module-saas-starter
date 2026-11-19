"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Ban, Link2, RefreshCw } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
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
	ErrorState,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import {
	formatDeliveryStatus,
	formatInvitationRole,
	formatInvitationStatus,
} from "../model/transforms";
import type { Invitation } from "../model/types";
import {
	useIssueInvitationLink,
	useResendInvitation,
	useRevokeInvitation,
} from "../service/mutations";
import { InvitationLinkDialog } from "./invitation-link-dialog";

const col = createColumnHelper<Invitation>();

export function InvitationsTable({
	invitations,
	isLoading,
	canManage = false,
}: {
	invitations: Invitation[];
	isLoading: boolean;
	canManage?: boolean;
}) {
	"use no memo";
	const revokeInvitation = useRevokeInvitation();
	const [confirming, setConfirming] = useState<Invitation | null>(null);
	const [failure, setFailure] = useState<string | null>(null);
	const submitting = useRef(false);
	const { mutate: resend, isPending: resending } = useResendInvitation();
	const { mutate: createLink, isPending: creatingLink } =
		useIssueInvitationLink();
	const [link, setLink] = useState<{ url: string; email: string } | null>(null);

	const columns = useMemo(
		() => [
			col.accessor("email", { header: "Email" }),
			col.accessor("role", {
				header: "Role",
				cell: (info) => (
					<Badge variant="outline" className="capitalize">
						{formatInvitationRole(info.getValue())}
					</Badge>
				),
			}),
			col.accessor("deliveryStatus", {
				header: "Delivery",
				cell: (info) => formatDeliveryStatus(info.getValue()),
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
					if (!canManage) return null;
					if (inv.status !== 1) return null;
					return (
						<div className="flex justify-end gap-1">
							<Button
								variant="ghost"
								size="sm"
								aria-label={`Copy invitation link for ${inv.email}`}
								disabled={creatingLink}
								onClick={() =>
									createLink(inv.id, {
										onSuccess: (response) =>
											setLink({ url: response.acceptUrl, email: inv.email }),
										onError: (error) =>
											toast.error(
												failureText("create an invitation link", error),
											),
									})
								}
							>
								<Link2 className="h-4 w-4" />
							</Button>
							<Button
								variant="ghost"
								size="sm"
								aria-label={`Resend invitation to ${inv.email}`}
								disabled={resending}
								onClick={() =>
									resend(inv.id, {
										onSuccess: () => toast.success("Invitation resent"),
										onError: (error) =>
											toast.error(failureText("resend the invitation", error)),
									})
								}
							>
								<RefreshCw className="h-4 w-4" />
							</Button>
							<Button
								variant="ghost"
								size="sm"
								aria-label={`Revoke invitation to ${inv.email}`}
								onClick={() => {
									setFailure(null);
									setConfirming(inv);
								}}
							>
								<Ban className="h-4 w-4 text-destructive" />
							</Button>
						</div>
					);
				},
			}),
		],
		[createLink, creatingLink, resend, resending, canManage],
	);

	const table = useReactTable({
		data: invitations,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<>
			<DataTable
				table={table}
				isLoading={isLoading}
				ariaLabel="Invitations"
				emptyMessage={
					canManage
						? "No invitations yet. Invite someone to join this organization."
						: "No invitations yet. An organization administrator can invite people."
				}
			/>
			<AlertDialog
				open={confirming !== null}
				onOpenChange={(open) => {
					if (!open && !submitting.current) {
						setConfirming(null);
						setFailure(null);
					}
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							Revoke invitation to {confirming?.email}?
						</AlertDialogTitle>
						<AlertDialogDescription>
							This person will no longer be able to join the organization using
							this invitation.
						</AlertDialogDescription>
					</AlertDialogHeader>
					{failure && (
						<ErrorState title="Invitation not revoked" detail={failure} />
					)}
					<AlertDialogFooter>
						<AlertDialogCancel disabled={revokeInvitation.isPending}>
							Cancel
						</AlertDialogCancel>
						<AlertDialogAction
							variant="destructive"
							disabled={revokeInvitation.isPending}
							onClick={(event) => {
								event.preventDefault();
								if (!confirming || !canManage || submitting.current) return;
								submitting.current = true;
								setFailure(null);
								revokeInvitation.mutate(confirming.id, {
									onSuccess: () => {
										toast.success("Invitation revoked");
										setConfirming(null);
									},
									onError: (error) =>
										setFailure(failureText("revoke the invitation", error)),
									onSettled: () => {
										submitting.current = false;
									},
								});
							}}
						>
							{revokeInvitation.isPending ? "Revoking…" : "Revoke"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
			<InvitationLinkDialog
				url={link?.url ?? null}
				email={link?.email ?? ""}
				onClose={() => setLink(null)}
			/>
		</>
	);
}
