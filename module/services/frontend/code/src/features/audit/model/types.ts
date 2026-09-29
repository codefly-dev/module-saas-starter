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
	/**
	 * The set form of eventType: a record matches when its type is any one of
	 * them. Both apply when both are sent.
	 *
	 * It is what lets a summary over a *family* of types be opened. "New users"
	 * counts every type the registry marks as recording a person joining — a set
	 * the registry owns and may extend — and a scalar eventType cannot name a
	 * set, so that tile was a figure with no way to check it.
	 */
	eventTypes?: string[];
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
	/** The registry's own answer to "does this event mean a person joined". */
	marksUserJoined: boolean;
	/**
	 * Whether an outbound webhook endpoint may subscribe to this type and ever
	 * receive it. The registry owns the answer — a platform type is eligible,
	 * and a solution- or module-declared type only when its producer declared
	 * it external and the operator granted its namespace external delivery — so
	 * the subscription form offers exactly the names that can fire instead of
	 * carrying a list of its own.
	 */
	webhookEligible: boolean;
}
