"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { useAuth } from "@/lib/auth";
import { Button } from "@/shared/ui";
import { notificationMutations } from "../service/mutations";
import { notificationQueries } from "../service/queries";

/**
 * Presents the newest unread notification for the active organization in the
 * host shell. Modules choose notification content; the host owns the live,
 * tenant-scoped presentation and unread lifecycle.
 */
export function NotificationBanner() {
	const { organizationId = "" } = useAuth();
	const queryClient = useQueryClient();
	const router = useRouter();
	const { data } = useQuery(notificationQueries.list(100));
	const notification = data?.notifications.find(
		(item) => !item.read && item.orgId === organizationId,
	);

	const markRead = useMutation({
		mutationFn: (id: string) => notificationMutations.markRead(id),
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: ["notifications"] }),
		onError: () => toast.error("Could not dismiss notification"),
	});
	const resolveAction = useMutation({
		mutationFn: (id: string) => notificationMutations.resolveAction(id),
		onSuccess: (actionUrl, id) => {
			markRead.mutate(id);
			router.push(actionUrl);
		},
		onError: () => toast.error("This notification is no longer available"),
	});

	if (!organizationId || !notification) return null;

	return (
		<div
			role="status"
			aria-live="polite"
			className="mx-6 mt-4 flex items-center justify-between gap-4 rounded-lg border border-primary/30 bg-primary/5 px-4 py-3 text-sm"
		>
			<div className="min-w-0">
				<p className="font-medium text-foreground">{notification.title}</p>
				<p className="text-muted-foreground">{notification.body}</p>
			</div>
			<div className="flex shrink-0 items-center gap-2">
				{notification.hasAction && (
					<Button
						variant="link"
						size="sm"
						disabled={resolveAction.isPending}
						onClick={() => resolveAction.mutate(notification.id)}
					>
						View
					</Button>
				)}
				<Button
					variant="ghost"
					size="sm"
					className="h-8 w-8 p-0"
					disabled={markRead.isPending}
					onClick={() => markRead.mutate(notification.id)}
					aria-label="Dismiss notification"
				>
					<X className="h-4 w-4" />
				</Button>
			</div>
		</div>
	);
}
