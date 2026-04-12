import type { UserStatus } from "./types";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

export function getStatusBadgeVariant(status: UserStatus): BadgeVariant {
  switch (status) {
    case "active": return "default";
    case "inactive": return "secondary";
    case "suspended": return "destructive";
    case "deleted": return "outline";
    default: return "secondary";
  }
}

export function statusLabel(status: UserStatus): string {
  switch (status) {
    case "active": return "Active";
    case "inactive": return "Inactive";
    case "suspended": return "Suspended";
    case "deleted": return "Deleted";
    default: return "Unknown";
  }
}

export function toDisplayName(profile: Record<string, string>, email: string): string {
  const first = profile["first_name"] ?? "";
  const last = profile["last_name"] ?? "";
  const full = [first, last].filter(Boolean).join(" ");
  return full || email;
}

export function sortByCreated<T extends { createdAt: string | undefined }>(items: T[]): T[] {
  return [...items].sort((a, b) => {
    if (!a.createdAt) return 1;
    if (!b.createdAt) return -1;
    return new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime();
  });
}
