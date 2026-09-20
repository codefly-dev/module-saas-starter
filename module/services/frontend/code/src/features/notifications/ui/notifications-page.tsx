"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { CheckCheck, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { Badge, Button, Card, CardContent, Skeleton } from "@/shared/ui";
import { formatNotificationType, timeAgo } from "../model/transforms";
import { notificationMutations } from "../service/mutations";
import { notificationQueries } from "../service/queries";

export function NotificationsPage() {
	const queryClient = useQueryClient();
	const router = useRouter();
	const [pages, setPages] = useState<string[]>([""]);
	const { data, isLoading, error } = useQuery(
		notificationQueries.list(50, { pageToken: pages[pages.length - 1] }),
	);
	const notifications = data?.notifications ?? [];

	const markReadMutation = useMutation({
		mutationFn: (id: string) => notificationMutations.markRead(id),
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: ["notifications"] }),
	});

	const markAllReadMutation = useMutation({
		mutationFn: () => notificationMutations.markAllRead(),
		onSuccess: () => {
			toast.success("All notifications marked as read");
			queryClient.invalidateQueries({ queryKey: ["notifications"] });
		},
		onError: () => toast.error("Failed to mark all as read"),
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
			toast.success("Notification deleted");
			queryClient.invalidateQueries({ queryKey: ["notifications"] });
		},
		onError: () => toast.error("Failed to delete notification"),
	});

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<h2 data-slot="page-title" className="type-page-title">Notifications</h2>
				<Button
					variant="outline"
					size="sm"
					onClick={() => markAllReadMutation.mutate()}
					disabled={markAllReadMutation.isPending}
				>
					<CheckCheck className="mr-2 h-4 w-4" />
					Mark all read
				</Button>
			</div>

			{error && (
				<p role="alert">Could not load notifications. Please try again.</p>
			)}
			{isLoading ? (
				<div className="space-y-3">
					{Array.from({ length: 5 }).map((_, i) => (
						<Skeleton key={i} className="h-20 w-full" />
					))}
				</div>
			) : notifications.length === 0 ? (
				<Card>
					<CardContent className="py-12 text-center">
						<p className="text-muted-foreground">No notifications yet.</p>
					</CardContent>
				</Card>
			) : (
				<div className="space-y-3">
					{notifications.map((notification) => (
						<Card
							key={notification.id}
							className={
								!notification.read ? "border-primary/30 bg-muted/20" : ""
							}
						>
							<CardContent className="flex items-start justify-between py-4">
								<button
									type="button"
									className="flex-1 text-left"
									onClick={() => {
										if (notification.hasAction) {
											resolveActionMutation.mutate({
												id: notification.id,
												unread: !notification.read,
											});
											return;
										}
										if (!notification.read) {
											markReadMutation.mutate(notification.id);
										}
									}}
								>
									<div className="flex items-center gap-2 mb-1">
										{!notification.read && (
											<span className="h-2 w-2 rounded-full bg-primary" />
										)}
										<span className="text-sm font-medium">
											{notification.title}
										</span>
										<Badge variant="outline" className="text-xs">
											{formatNotificationType(notification.type)}
										</Badge>
									</div>
									<p className="text-sm text-muted-foreground">
										{notification.body}
									</p>
									<p className="mt-1 text-xs text-muted-foreground">
										{timeAgo(notification.createdAt)}
									</p>
								</button>
								<Button
									variant="ghost"
									size="sm"
									className="h-8 w-8 p-0 shrink-0"
									onClick={() => deleteMutation.mutate(notification.id)}
								>
									<Trash2 className="h-4 w-4" />
								</Button>
							</CardContent>
						</Card>
					))}
				</div>
			)}
			<div className="flex gap-2">
				{pages.length > 1 && (
					<Button
						variant="outline"
						onClick={() => setPages((current) => current.slice(0, -1))}
					>
						Newer notifications
					</Button>
				)}
				{data?.nextPageToken && (
					<Button
						variant="outline"
						onClick={() =>
							setPages((current) => [...current, data.nextPageToken])
						}
					>
						Older notifications
					</Button>
				)}
			</div>
		</div>
	);
}
