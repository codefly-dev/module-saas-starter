"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCheck, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import {
	Badge,
	Button,
	Card,
	CardContent,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Skeleton,
} from "@/shared/ui";
import { formatNotificationType, timeAgo } from "../model/transforms";
import { notificationMutations } from "../service/mutations";
import { notificationQueries } from "../service/queries";

export function NotificationsPage() {
	const queryClient = useQueryClient();
	const router = useRouter();
	const [pages, setPages] = useState<string[]>([""]);
	const { data, isLoading, isError } = useQuery(
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
			toast.success("Notification deleted");
			queryClient.invalidateQueries({ queryKey: ["notifications"] });
		},
		onError: (error) =>
			toast.error(failureText("delete the notification", error)),
	});

	return (
		<PageBody>
			<PageHeader
				title="Notifications"
				description="Invitations, security messages and updates sent to you."
				actions={
					<Button
						variant="outline"
						onClick={() => markAllReadMutation.mutate()}
						disabled={markAllReadMutation.isPending}
					>
						<CheckCheck />
						Mark all read
					</Button>
				}
			/>

			{/* A failed read is reported on its own: "No notifications yet" beside
			    it would answer a question the page could not ask. */}
			{isError ? (
				<Panel>
					<ErrorState
						title="Couldn't load your notifications"
						detail="Whether you have any is unknown. Reload the page; if it keeps failing, tell your administrator."
					/>
				</Panel>
			) : isLoading ? (
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
							<CardContent className="flex items-start justify-between gap-2">
								<Button
									variant="ghost"
									className="h-auto flex-1 flex-col items-start justify-start gap-0 whitespace-normal px-2 py-1 text-left font-normal"
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
									<span className="mb-1 flex items-center gap-2">
										{!notification.read && (
											<span
												aria-hidden="true"
												className="size-2 rounded-full bg-primary"
											/>
										)}
										<span className="text-sm font-medium">
											{notification.title}
										</span>
										<Badge variant="outline">
											{formatNotificationType(notification.type)}
										</Badge>
									</span>
									<span className="block text-sm text-muted-foreground">
										{notification.body}
									</span>
									<span className="mt-1 block text-xs text-muted-foreground">
										{timeAgo(notification.createdAt)}
									</span>
								</Button>
								<Button
									variant="ghost"
									size="icon-sm"
									className="shrink-0"
									aria-label="Delete notification"
									onClick={() => deleteMutation.mutate(notification.id)}
								>
									<Trash2 />
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
		</PageBody>
	);
}
