// Pure domain types for audit log. No React imports.

export interface AuditEvent {
	id: string;
	actorId: string;
	actorType: string;
	action: string;
	resource: string;
	resourceId: string;
	orgId: string;
	metadata: Record<string, string>;
	ipAddress: string;
	createdAt?: string;
}

export interface AuditLogFilters {
	orgId?: string;
	action?: string;
	actorId?: string;
	pageSize?: number;
}

export const AUDIT_ACTION_TYPES = [
	"user.registered",
	"user.suspended",
	"user.unsuspended",
	"auth.login",
	"auth.logout",
	"api_key.created",
	"api_key.revoked",
	"role.assigned",
	"role.revoked",
	"invitation.created",
	"invitation.accepted",
	"org.created",
	"org.member_added",
	"org.member_removed",
	"platform.role_granted",
	"platform.role_revoked",
	"platform.user_impersonated",
	"feature_flag.updated",
] as const;
