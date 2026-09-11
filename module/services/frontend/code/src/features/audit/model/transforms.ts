import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { AuditEvent as ProtoAuditEvent } from "@/gen/saas/accounts/v1/audit_pb";
import { truncateUUID } from "@/shared/lib/utils";
import type { AuditEvent, PrincipalDirectory } from "./types";

// toAuditEvent maps a wire (protobuf-es) audit event to the pure domain model.
// The one load-bearing conversion is `created_at`: over protobuf-es it arrives
// as a google.protobuf.Timestamp object ({ seconds, nanos }), NOT a string —
// rendering that object directly throws "Objects are not valid as a React
// child". We normalize it to an ISO string here so the table (and every other
// consumer typed against the model) gets the `string` it expects. Do this at
// the query boundary, never with an `as AuditEvent[]` cast — the cast is what
// let this bug reach production undetected.
export function toAuditEvent(e: ProtoAuditEvent): AuditEvent {
	return {
		id: e.id,
		actorId: e.actorId,
		actorType: e.actorType,
		eventType: e.eventType,
		schemaVersion: e.schemaVersion,
		category: e.category,
		resource: e.resource,
		resourceId: e.resourceId,
		orgId: e.orgId,
		payload: e.payload,
		ipAddress: e.ipAddress,
		createdAt: e.createdAt
			? timestampDate(e.createdAt).toISOString()
			: undefined,
	};
}

// Event types are `<namespace>.<aggregate>.<event>`. The namespace identifies the
// module that minted the type, not the action, so humanizing the whole string
// would render "Saas Auth Login"; strip it and render the action alone. The
// owning module is surfaced as its own filter facet, not folded into the label.
export function formatAuditAction(action: string): string {
	return auditEventAction(action)
		.replace(/[._]/g, " ")
		.replace(/\b\w/g, (c) => c.toUpperCase());
}

// auditEventAction returns the event type without its namespace segment.
export function auditEventAction(eventType: string): string {
	const dot = eventType.indexOf(".");
	return dot === -1 ? eventType : eventType.slice(dot + 1);
}

export interface ResolvedActor {
	label: string;
	// Whether `label` is a principal's name rather than a fallback. Presence in
	// the directory is NOT the same question: principals.display_name is NOT
	// NULL but otherwise unconstrained, so a row can carry "". Callers that
	// style or sort by "did this resolve" must read this, not `directory.has`,
	// or the two answers drift apart on exactly that row.
	resolved: boolean;
}

// resolveActor renders an audit row's actor as a person, service, or agent
// rather than as an opaque id. A principal that is revoked, cross-org, or past
// the directory's page walk is absent from the map, so the fallback is the
// truncated id: an unresolved actor must still read as *an* actor.
export function resolveActor(
	actorId: string,
	directory: PrincipalDirectory,
): ResolvedActor {
	const displayName = directory.get(actorId);
	if (displayName) return { label: displayName, resolved: true };
	// A row with no actor id is automated work the server attributed to no
	// principal; truncating "" would leave the cell blank.
	if (!actorId) return { label: "System", resolved: false };
	return { label: truncateUUID(actorId), resolved: false };
}

// formatActorType renders the audit row's actor_type facet — one of user,
// api_key, system, agent. It stays beside the resolved name because reading an
// agent's action as a human's is worse than reading a uuid.
export function formatActorType(actorType: string): string {
	return actorType.replace(/_/g, " ");
}

export interface AuditGroup {
	date: string;
	events: AuditEvent[];
}

export function groupByDate(events: AuditEvent[]): AuditGroup[] {
	const map = new Map<string, AuditEvent[]>();
	for (const event of events) {
		const date = event.createdAt
			? new Date(event.createdAt).toLocaleDateString("en-US", {
					year: "numeric",
					month: "long",
					day: "numeric",
				})
			: "Unknown Date";
		if (!map.has(date)) map.set(date, []);
		map.get(date)!.push(event);
	}
	return Array.from(map.entries()).map(([date, events]) => ({ date, events }));
}
