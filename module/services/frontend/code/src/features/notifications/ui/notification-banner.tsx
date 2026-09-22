"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { toast } from "sonner";
import { useAuth } from "@/lib/auth";
import { Banner, Button } from "@/shared/ui";
import { notificationMutations } from "../service/mutations";
import { notificationQueries } from "../service/queries";

/**
 * Presents the newest unread notification for the active organization in the
 * host shell. Modules choose notification content; the host owns the live,
 * tenant-scoped presentation and unread lifecycle.
 */
export function NotificationBanner() {
	const { organizationId = "", user } = useAuth();
	const queryClient = useQueryClient();
	const router = useRouter();
	const { data } = useQuery({
		...notificationQueries.list(1, {
			orgId: organizationId,
			unreadOnly: true,
			userId: user?.id,
			firstVisible: true,
		}),
		enabled: Boolean(organizationId),
	});
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
		<Banner
			className="mx-6 mt-4"
			title={notification.title}
			onDismiss={() => markRead.mutate(notification.id)}
			dismissDisabled={markRead.isPending}
			actions={
				notification.hasAction ? (
					<Button
						variant="link"
						size="sm"
						disabled={resolveAction.isPending}
						onClick={() => resolveAction.mutate(notification.id)}
					>
						View
					</Button>
				) : undefined
			}
		>
			{notification.body}
		</Banner>
	);
}
