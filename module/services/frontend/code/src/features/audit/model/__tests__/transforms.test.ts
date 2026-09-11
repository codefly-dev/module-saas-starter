import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { AuditEventSchema } from "@/gen/saas/accounts/v1/audit_pb";
import {
	auditEventAction,
	formatActorType,
	formatAuditAction,
	groupByDate,
	resolveActor,
	toAuditEvent,
} from "../transforms";
import type { AuditEvent } from "../types";

describe("formatAuditAction", () => {
	// The namespace names the module that minted the type, not what happened, so
	// it must not reach the label — "Saas Auth Login" reads as a typo.
	it("strips the namespace and title-cases the action", () => {
		expect(formatAuditAction("saas.auth.login")).toBe("Auth Login");
	});

	it("converts dot-separated action to title case", () => {
		expect(formatAuditAction("saas.user.registered")).toBe("User Registered");
	});

	it("converts underscore-separated action to title case", () => {
		expect(formatAuditAction("saas.api_key.created")).toBe("Api Key Created");
	});

	it("keeps a deeper aggregate intact", () => {
		expect(formatAuditAction("saas.datasource.source.added")).toBe(
			"Datasource Source Added",
		);
	});

	it("handles single word", () => {
		expect(formatAuditAction("login")).toBe("Login");
	});

	it("handles already formatted string", () => {
		expect(formatAuditAction("Hello World")).toBe("Hello World");
	});
});

describe("auditEventAction", () => {
	it("strips the namespace segment", () => {
		expect(auditEventAction("saas.auth.login")).toBe("auth.login");
	});

	it("passes a bare value through", () => {
		expect(auditEventAction("login")).toBe("login");
	});
});

describe("resolveActor", () => {
	const directory = new Map([
		["a3f81c2e-0000-4000-8000-000000000001", "Ada Lovelace"],
		["a3f81c2e-0000-4000-8000-000000000002", "deploy-bot"],
		// principals.display_name is NOT NULL but carries no non-empty CHECK.
		["a3f81c2e-0000-4000-8000-000000000004", ""],
	]);

	it("renders a resolved principal by its display name", () => {
		expect(
			resolveActor("a3f81c2e-0000-4000-8000-000000000001", directory),
		).toEqual({ label: "Ada Lovelace", resolved: true });
	});

	it("resolves a non-human principal by name too", () => {
		expect(
			resolveActor("a3f81c2e-0000-4000-8000-000000000002", directory).label,
		).toBe("deploy-bot");
	});

	// A revoked or cross-org principal is absent from the directory. The row
	// must still read as an actor, so it falls back to the truncated id rather
	// than to a blank cell.
	it("falls back to the truncated id for an unresolved principal", () => {
		expect(
			resolveActor("b7c22d10-0000-4000-8000-000000000003", directory),
		).toEqual({ label: "b7c22d10...", resolved: false });
	});

	// The bug this pins: `directory.has(id)` says "resolved" for an empty
	// display_name while the label falls back to the id, so a caller styling or
	// sorting off `has` disagreed with what the cell rendered.
	it("reports an empty display name as unresolved, not as a name", () => {
		expect(
			resolveActor("a3f81c2e-0000-4000-8000-000000000004", directory),
		).toEqual({ label: "a3f81c2e...", resolved: false });
	});

	it("names an actor-less row rather than rendering blank", () => {
		expect(resolveActor("", directory)).toEqual({
			label: "System",
			resolved: false,
		});
	});

	it("falls back when the directory has not landed yet", () => {
		expect(
			resolveActor("a3f81c2e-0000-4000-8000-000000000001", new Map()).label,
		).toBe("a3f81c2e...");
	});
});

describe("formatActorType", () => {
	it("renders the underscored wire value as words", () => {
		expect(formatActorType("api_key")).toBe("api key");
	});

	it("passes a single-word type through", () => {
		expect(formatActorType("agent")).toBe("agent");
	});
});

function makeEvent(overrides: Partial<AuditEvent> = {}): AuditEvent {
	return {
		id: "evt-1",
		actorId: "user-1",
		actorType: "user",
		eventType: "saas.user.registered",
		schemaVersion: 1,
		category: "auth",
		resource: "user",
		resourceId: "user-1",
		orgId: "org-1",
		payload: {},
		ipAddress: "127.0.0.1",
		...overrides,
	};
}

describe("groupByDate", () => {
	it("groups events by their date", () => {
		const events = [
			makeEvent({ id: "1", createdAt: "2024-06-15T10:00:00Z" }),
			makeEvent({ id: "2", createdAt: "2024-06-15T14:00:00Z" }),
			makeEvent({ id: "3", createdAt: "2024-06-16T09:00:00Z" }),
		];
		const groups = groupByDate(events);
		expect(groups).toHaveLength(2);
		// The group with 2 events from June 15
		const june15 = groups.find((g) => g.events.length === 2);
		expect(june15).toBeDefined();
		expect(june15!.events.map((e) => e.id)).toEqual(["1", "2"]);
	});

	it("returns empty array for empty input", () => {
		expect(groupByDate([])).toEqual([]);
	});

	it("uses 'Unknown Date' for events without createdAt", () => {
		const events = [makeEvent({ id: "1" })]; // no createdAt
		const groups = groupByDate(events);
		expect(groups).toHaveLength(1);
		expect(groups[0].date).toBe("Unknown Date");
	});

	it("preserves event order within a group", () => {
		const events = [
			makeEvent({ id: "a", createdAt: "2024-01-01T08:00:00Z" }),
			makeEvent({ id: "b", createdAt: "2024-01-01T12:00:00Z" }),
			makeEvent({ id: "c", createdAt: "2024-01-01T16:00:00Z" }),
		];
		const groups = groupByDate(events);
		expect(groups[0].events.map((e) => e.id)).toEqual(["a", "b", "c"]);
	});
});

describe("toAuditEvent", () => {
	// Regression guard for the "Objects are not valid as a React child
	// ({$typeName, seconds, nanos})" crash: over protobuf-es, created_at arrives
	// as a google.protobuf.Timestamp OBJECT, not a string. This test builds an
	// event the exact way the wire does (create + timestampFromDate) so the real
	// proto shape is exercised — the previous string-only fixtures never were.
	it("normalizes the protobuf Timestamp created_at to an ISO string", () => {
		const proto = create(AuditEventSchema, {
			id: "evt-1",
			actorId: "user-1",
			actorType: "user",
			eventType: "saas.auth.login",
			schemaVersion: 1,
			category: "security",
			resource: "session",
			resourceId: "sess-1",
			orgId: "org-1",
			ipAddress: "127.0.0.1",
			createdAt: timestampFromDate(new Date("2026-08-24T20:58:52.000Z")),
		});

		const model = toAuditEvent(proto);

		expect(typeof model.createdAt).toBe("string");
		expect(model.createdAt).toBe("2026-08-24T20:58:52.000Z");
		// The rest of the fields pass through unchanged.
		expect(model.id).toBe("evt-1");
		expect(model.eventType).toBe("saas.auth.login");
	});

	it("leaves created_at undefined when the Timestamp is absent", () => {
		const proto = create(AuditEventSchema, { id: "evt-2" });
		expect(toAuditEvent(proto).createdAt).toBeUndefined();
	});
});
