import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { AuditEvent as ProtoAuditEvent } from "@/gen/saas/accounts/v1/audit_pb";
import type { AuditEvent } from "./types";

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

export function formatAuditAction(action: string): string {
	return action.replace(/[._]/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
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
