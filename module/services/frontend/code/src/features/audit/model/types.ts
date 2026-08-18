// Pure domain types for audit log. No React imports.

export interface AuditEvent {
	id: string;
	actorId: string;
	actorType: string;
	eventType: string;
	schemaVersion: number;
	category: string;
	resource: string;
	resourceId: string;
	orgId: string;
	payload?: Record<string, unknown>;
	ipAddress: string;
	createdAt?: string;
}

export interface AuditLogFilters {
	orgId?: string;
	eventType?: string;
	category?: string;
	actorId?: string;
	pageSize?: number;
}

// A registered audit event type, from the AuditService/ListAuditEventTypes RPC.
// The registry is server-owned; the UI facet is a projection of it rather than
// a hand-maintained list.
export interface AuditEventTypeInfo {
	name: string;
	version: number;
	category: string;
	owner: string;
	deprecated: boolean;
	description: string;
}
