import { describe, expect, it } from "vitest";
import { formatAuditAction, groupByDate } from "../transforms";
import type { AuditEvent } from "../types";

describe("formatAuditAction", () => {
	it("converts dot-separated action to title case", () => {
		expect(formatAuditAction("user.registered")).toBe("User Registered");
	});

	it("converts underscore-separated action to title case", () => {
		expect(formatAuditAction("api_key.created")).toBe("Api Key Created");
	});

	it("handles single word", () => {
		expect(formatAuditAction("login")).toBe("Login");
	});

	it("handles mixed separators", () => {
		expect(formatAuditAction("org.member_added")).toBe("Org Member Added");
	});

	it("handles already formatted string", () => {
		expect(formatAuditAction("Hello World")).toBe("Hello World");
	});
});

function makeEvent(overrides: Partial<AuditEvent> = {}): AuditEvent {
	return {
		id: "evt-1",
		actorId: "user-1",
		actorType: "user",
		eventType: "user.registered",
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
