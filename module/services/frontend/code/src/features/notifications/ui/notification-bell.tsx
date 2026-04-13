"use client";

import { useState, useRef, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bell } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/shared/ui";
import { notificationQueries } from "../service/queries";
import { NotificationPanel } from "./notification-panel";

export function NotificationBell() {
  const [open, setOpen] = useState(false);
  const { data } = useQuery(notificationQueries.unreadCount());
  const unreadCount = (data as { count?: number } | undefined)?.count ?? 0;
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
