import type { TeamRole } from "./types";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

export function getRoleBadgeVariant(role: TeamRole): BadgeVariant {
	switch (role) {
		case "owner":
			return "default";
		case "admin":
			return "secondary";
		case "member":
			return "outline";
		default:
			return "outline";
	}
}

export function roleLabel(role: TeamRole): string {
	switch (role) {
		case "owner":
			return "Owner";
		case "admin":
			return "Admin";
		case "member":
			return "Member";
		default:
			return "Unknown";
	}
}
