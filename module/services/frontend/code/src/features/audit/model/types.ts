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
	// The registered client the call was made through, empty for a call made
	// from the host's own web session. Separate from actorId: one answers who
	// acted, the other what they acted through.
	clientId: string;
	createdAt?: string;
}

// PrincipalDirectory maps a principal id to the display name the audit
// surfaces render for it. Built from one ListPrincipals walk per org, so a
// table of N rows costs no lookups of its own.
export type PrincipalDirectory = ReadonlyMap<string, string>;

export interface AuditLogFilters {
	orgId?: string;
	eventType?: string;
	category?: string;
	namespace?: string;
	actorId?: string;
	// from/to bound the window; omit for all-time.
	from?: Date;
	to?: Date;
	pageSize?: number;
}

// A registered audit event type, from the AuditService/ListAuditEventTypes RPC.
// The registry is server-owned; the UI facet is a projection of it rather than
// a hand-maintained list.
export interface AuditEventTypeInfo {
	name: string;
	namespace: string;
	version: number;
	category: string;
	owner: string;
	deprecated: boolean;
	description: string;
}
