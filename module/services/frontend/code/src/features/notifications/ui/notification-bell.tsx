"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { useAuth } from "@/lib/auth";
import { Button } from "@/shared/ui";
import { notificationQueries } from "../service/queries";
import { NotificationPanel } from "./notification-panel";

/**
 * Connects with streaming fetch so the token remains in Authorization and
 * never enters URLs, logs, browser history, or referrer metadata. Falls back
 * to react-query polling (30s) if the stream errors out.
 */
function useNotificationSSE(onCount: (count: number) => void) {
	const { getToken } = useAuth();
	useEffect(() => {
		const controller = new AbortController();
		const token = getToken();
		const connect = async () => {
			try {
				const response = await fetch("/api/notifications/stream", {
					headers: token ? { Authorization: `Bearer ${token}` } : undefined,
					signal: controller.signal,
				});
				if (!response.ok || !response.body)
					throw new Error("notification stream unavailable");

				const reader = response.body.getReader();
				const decoder = new TextDecoder();
				let buffer = "";
				while (!controller.signal.aborted) {
					const { value, done } = await reader.read();
					if (done) break;
					buffer += decoder.decode(value, { stream: true });
					const events = buffer.split("\n\n");
					buffer = events.pop() ?? "";
					for (const event of events) {
						const line = event
							.split("\n")
							.find((candidate) => candidate.startsWith("data:"));
						if (!line) continue;
						const data = JSON.parse(line.slice(5).trim()) as {
							unreadCount?: number;
						};
						if (typeof data.unreadCount === "number") onCount(data.unreadCount);
					}
				}
			} catch {
				// Polling remains enabled while no stream value has arrived.
			}
		};
		void connect();

		return () => controller.abort();
	}, [getToken, onCount]);
}

export function NotificationBell() {
	const [open, setOpen] = useState(false);
	const [sseCount, setSseCount] = useState<number | null>(null);
	const queryClient = useQueryClient();

	// Polling fallback — will be used when SSE is unavailable.
	const { data } = useQuery({
		...notificationQueries.unreadCount(),
		// Disable aggressive refetch when SSE is delivering updates.
		refetchInterval: sseCount !== null ? false : 30_000,
	});

	const handleSSECount = useCallback(
		(count: number) => {
			setSseCount(count);
			// Keep the query cache in sync so NotificationPanel sees fresh data.
			queryClient.setQueryData(["notifications", "unread-count"], {
				count,
			});
		},
		[queryClient],
	);

	useNotificationSSE(handleSSECount);

	const polledCount = (data as { count?: number } | undefined)?.count ?? 0;
	const unreadCount = sseCount ?? polledCount;
	const prevCountRef = useRef(unreadCount);

	useEffect(() => {
		if (unreadCount > prevCountRef.current && prevCountRef.current >= 0) {
			const diff = unreadCount - prevCountRef.current;
			toast.info(
				diff === 1
					? "You have a new notification"
					: `You have ${diff} new notifications`,
			);
		}
		prevCountRef.current = unreadCount;
	}, [unreadCount]);

	return (
		<div className="relative">
			<Button
				variant="ghost"
				size="sm"
				className="relative h-8 w-8 p-0"
				onClick={() => setOpen(!open)}
			>
				<Bell className="h-4 w-4" />
				{unreadCount > 0 && (
					<span className="absolute -right-1 -top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-medium text-destructive-foreground">
						{unreadCount > 99 ? "99+" : unreadCount}
					</span>
				)}
			</Button>

			{open && <NotificationPanel onClose={() => setOpen(false)} />}
		</div>
	);
}
