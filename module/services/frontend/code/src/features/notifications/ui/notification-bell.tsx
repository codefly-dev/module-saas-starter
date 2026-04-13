"use client";

import { useState, useRef, useEffect, useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/shared/ui";
import { useAuth } from "@/lib/auth";
import { notificationQueries } from "../service/queries";
import { NotificationPanel } from "./notification-panel";

/**
 * Connects to the SSE endpoint for real-time unread count updates.
 * Falls back to react-query polling (30s) if EventSource is
 * unavailable or the stream errors out.
 */
function useNotificationSSE(onCount: (count: number) => void) {
  const { getToken } = useAuth();
  const fallbackRef = useRef(false);

  useEffect(() => {
    // EventSource doesn't support custom headers natively. We pass the
    // token as a query parameter so the Next.js API route can forward it.
    // The route itself is a server-side proxy, so the real auth header
    // stays internal.
    if (typeof EventSource === "undefined") {
      fallbackRef.current = true;
      return;
    }

    const token = getToken();
    const url = token
      ? `/api/notifications/stream?token=${encodeURIComponent(token)}`
      : "/api/notifications/stream";

    const es = new EventSource(url);

    es.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data) as { unreadCount?: number };
        if (typeof data.unreadCount === "number") {
          onCount(data.unreadCount);
        }
      } catch {
        // Ignore malformed messages
      }
    };

    es.onerror = () => {
      // On persistent failure, close and fall back to polling.
      es.close();
      fallbackRef.current = true;
    };

    return () => {
      es.close();
    };
  }, [getToken, onCount]);

  return fallbackRef;
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

      {open && (
        <NotificationPanel onClose={() => setOpen(false)} />
      )}
    </div>
  );
}
