/**
 * Domain-specific data transforms for user management.
 * These are specific to the user-management / saas-starter modules.
 */

export type StatusVariant = "success" | "warning" | "error" | "default";

export interface FormattedStatus {
	label: string;
	variant: StatusVariant;
}

export function formatUserStatus(status: string | undefined): FormattedStatus {
	const label = status?.replace("USER_STATUS_", "").toLowerCase() ?? "unknown";
	const variant: StatusVariant =
		status === "USER_STATUS_ACTIVE"
			? "success"
			: status === "USER_STATUS_SUSPENDED"
				? "error"
				: "default";
	return { label, variant };
}

export function formatInvitationStatus(
	status: string | undefined,
): FormattedStatus {
	const label = status ?? "unknown";
	const variant: StatusVariant =
		status === "pending"
			? "warning"
			: status === "accepted"
				? "success"
				: status === "expired" || status === "revoked"
					? "error"
					: "default";
	return { label, variant };
}

export function formatPlatformRole(role: string | undefined): string {
	if (!role) return "-";
	return role.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}
