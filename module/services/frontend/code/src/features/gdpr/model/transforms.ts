import type { GDPRRequestStatus, GDPRRequestType } from "./types";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline";

export function formatGDPRStatus(status: GDPRRequestStatus): {
	label: string;
	variant: BadgeVariant;
} {
	switch (status) {
		case "completed":
			return { label: "Completed", variant: "default" };
		case "pending":
			return { label: "Pending", variant: "secondary" };
		case "processing":
			return { label: "Processing", variant: "outline" };
		case "failed":
			return { label: "Failed", variant: "destructive" };
		case "cancelled":
			return { label: "Cancelled", variant: "secondary" };
		default:
			return { label: "Unknown", variant: "secondary" };
	}
}

export function formatRequestType(type: GDPRRequestType): string {
	switch (type) {
		case "export":
			return "Data Export";
		case "deletion":
			return "Account Deletion";
		default:
			return "Unknown";
	}
}
