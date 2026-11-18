"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { CheckCheck, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
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
	Page as PageBody,
	PageHeader,
	Panel,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { formatNotificationType, timeAgo } from "../model/transforms";
import type { Notification } from "../model/types";
import { notificationMutations } from "../service/mutations";
import { notificationQueries } from "../service/queries";

const NO_NOTIFICATIONS: Notification[] = [];
const column = createColumnHelper<Notification>();

export function NotificationsPage() {
	const queryClient = useQueryClient();
	const router = useRouter();
	const [pages, setPages] = useState<string[]>([""]);
	const { data, isLoading, isError, error, refetch } = useQuery(
		notificationQueries.list(50, { pageToken: pages[pages.length - 1] }),
	);
	const notifications = data?.notifications ?? NO_NOTIFICATIONS;

	const [deleteTarget, setDeleteTarget] = useState<Notification | null>(null);
	const deleting = useRef(false);
	const markReadMutation = useMutation({
		mutationFn: (id: string) => notificationMutations.markRead(id),
		onError: (error) =>
			toast.error(failureText("mark this notification as read", error)),
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: ["notifications"] }),
	});

	const markAllReadMutation = useMutation({
		mutationFn: () => notificationMutations.markAllRead(),
		onSuccess: () => {
			toast.success("All notifications marked as read");
			queryClient.invalidateQueries({ queryKey: ["notifications"] });
		},
		onError: (error) =>
			toast.error(failureText("mark your notifications as read", error)),
	});

	// Marking read is part of following the link, so it happens only once the
	// destination has been re-authorized. Marking first would let a click the
	// server then refuses still consume the item's unread state.
	const resolveActionMutation = useMutation({
		mutationFn: ({ id }: { id: string; unread: boolean }) =>
			notificationMutations.resolveAction(id),
		onSuccess: (actionUrl, { id, unread }) => {
			if (unread) {
				markReadMutation.mutate(id);
			}
			router.push(actionUrl);
		},
		onError: () => toast.error("This notification is no longer available"),
	});

	const deleteMutation = useMutation({
		mutationFn: (id: string) => notificationMutations.delete(id),
		onSuccess: () => {
			deleting.current = false;
			setDeleteTarget(null);
			toast.success("Notification deleted");
			queryClient.invalidateQueries({ queryKey: ["notifications"] });
		},
		onError: () => {
			deleting.current = false;
		},
	});

	const busy =
		markReadMutation.isPending ||
		markAllReadMutation.isPending ||
		resolveActionMutation.isPending ||
		deleteMutation.isPending;
	const columns = useMemo(
		() => [
			column.display({
				id: "message",
				header: "Notification",
				cell: ({ row }) => {
					const notification = row.original;
					return (
						<Button
							variant="ghost"
							className="h-auto whitespace-normal text-left"
							disabled={busy}
							onClick={() => {
								if (notification.hasAction)
									resolveActionMutation.mutate({
										id: notification.id,
										unread: !notification.read,
									});
								else if (!notification.read)
									markReadMutation.mutate(notification.id);
							}}
						>
							<span>
								<span className="block font-medium">{notification.title}</span>
								<span className="block text-muted-foreground">
									{notification.body}
								</span>
							</span>
						</Button>
					);
				},
			}),
			column.accessor("type", {
				header: "Category",
				cell: ({ getValue }) => (
					<Badge variant="outline">{formatNotificationType(getValue())}</Badge>
				),
			}),
			column.accessor("read", {
				header: "Status",
				cell: ({ getValue }) => (getValue() ? "Read" : "Unread"),
			}),
			column.accessor("createdAt", {
				header: "Received",
				cell: ({ getValue }) => timeAgo(getValue()),
			}),
			column.display({
				id: "actions",
				header: "",
				cell: ({ row }) => (
					<Button
						variant="ghost"
						size="icon-sm"
						aria-label={`Delete ${row.original.title}`}
						disabled={busy}
						onClick={() => {
							deleteMutation.reset();
							setDeleteTarget(row.original);
						}}
					>
						<Trash2 />
					</Button>
				),
			}),
		],
		[
			busy,
			resolveActionMutation.mutate,
			markReadMutation.mutate,
			deleteMutation.reset,
		],
	);
	const table = useReactTable({
		data: notifications,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
	});
	const denied = readOutcome(isError, error) === "forbidden";
	return (
		<PageBody>
			<PageHeader
				title="Notifications"
				description="Invitations, security messages and updates sent to you."
				actions={
					<Button
						variant="outline"
						onClick={() => markAllReadMutation.mutate()}
						disabled={
							busy ||
							isLoading ||
							isError ||
							!notifications.some((item) => !item.read)
						}
					>
						<CheckCheck />
						Mark all read
					</Button>
				}
			/>
			{isError ? (
				<Panel>
					<ErrorState
						title="Couldn't load your notifications"
						detail={
							denied
								? "You don’t have access to these notifications. Ask your administrator to grant access."
								: "Check your connection and try again."
						}
					/>
					{!denied && (
						<Button variant="outline" onClick={() => void refetch()}>
							Try again
						</Button>
					)}
				</Panel>
			) : (
				<DataTable
					table={table}
					isLoading={isLoading}
					emptyMessage="No notifications yet. Invitations, security messages and updates will appear here."
				/>
			)}
			<div className="flex gap-2">
				{pages.length > 1 && (
					<Button
						variant="outline"
						disabled={isLoading || busy}
						onClick={() => setPages((current) => current.slice(0, -1))}
					>
						Newer notifications
					</Button>
				)}
				{!isError && data?.nextPageToken && (
					<Button
						variant="outline"
						disabled={isLoading || busy}
						onClick={() =>
							setPages((current) => [...current, data.nextPageToken])
						}
					>
						Older notifications
					</Button>
				)}
			</div>
			{deleteTarget && (
				<AlertDialog
					open
					onOpenChange={(open) => {
						if (!open && !deleting.current) setDeleteTarget(null);
					}}
				>
					<AlertDialogContent>
						<AlertDialogHeader>
							<AlertDialogTitle>Delete notification?</AlertDialogTitle>
							<AlertDialogDescription>
								Delete “{deleteTarget.title}”? This notification will be removed
								from your inbox.
							</AlertDialogDescription>
						</AlertDialogHeader>
						{deleteMutation.isError && (
							<ErrorState
								title="Couldn’t delete this notification"
								detail={failureText(
									"delete the notification",
									deleteMutation.error,
								)}
							/>
						)}
						<AlertDialogFooter>
							<AlertDialogCancel disabled={deleteMutation.isPending}>
								Cancel
							</AlertDialogCancel>
							<AlertDialogAction
								variant="destructive"
								disabled={deleteMutation.isPending}
								onClick={(event) => {
									event.preventDefault();
									if (deleting.current) return;
									deleting.current = true;
									deleteMutation.mutate(deleteTarget.id);
								}}
							>
								{deleteMutation.isPending ? "Deleting…" : "Delete notification"}
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
			)}
		</PageBody>
	);
}
