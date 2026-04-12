import type { NotificationType } from "./types";

export function formatNotificationType(type: NotificationType): string {
  switch (type) {
    case "info":
      return "Info";
    case "success":
      return "Success";
    case "warning":
      return "Warning";
    case "error":
      return "Error";
    case "invite":
      return "Invitation";
    case "mention":
      return "Mention";
    case "system":
      return "System";
    default:
      return "Notification";
  }
}

/** Lucide icon name for each notification type. */
export function getNotificationIcon(type: NotificationType): string {
  switch (type) {
    case "info":
      return "Info";
    case "success":
      return "CheckCircle";
    case "warning":
      return "AlertTriangle";
    case "error":
      return "XCircle";
    case "invite":
      return "Mail";
    case "mention":
      return "AtSign";
    case "system":
      return "Settings";
    default:
      return "Bell";
  }
}

export function timeAgo(dateString: string): string {
  const now = Date.now();
  const then = new Date(dateString).getTime();
  const diffMs = now - then;

  const seconds = Math.floor(diffMs / 1000);
  if (seconds < 60) return "just now";

  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;

  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;

  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;

  const weeks = Math.floor(days / 7);
  if (weeks < 4) return `${weeks}w ago`;

  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
  }).format(new Date(dateString));
}
