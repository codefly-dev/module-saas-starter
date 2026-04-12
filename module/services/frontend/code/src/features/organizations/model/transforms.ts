import type { OrgRole } from "./types";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

export function getRoleBadgeVariant(role: OrgRole): BadgeVariant {
  switch (role) {
    case "owner": return "default";
    case "admin": return "secondary";
    case "member": return "outline";
    default: return "outline";
  }
}

export function roleLabel(role: OrgRole): string {
  switch (role) {
    case "owner": return "Owner";
    case "admin": return "Admin";
    case "member": return "Member";
    default: return "Unknown";
  }
}

export function slugify(name: string): string {
  return name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
}
