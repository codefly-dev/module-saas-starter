import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

// ActivityFeed consumes `useAuditLog`, whose `select` maps every row through
// `toAuditEvent` — so `createdAt` reaches this component already normalized to
// an ISO-8601 string (or undefined), never a protobuf { seconds } object. These
// tests pin that contract: the pre-fix code typed `createdAt` as { seconds } and
// ran Number(t.seconds) on the real string value → NaN → new Date(NaN) →
// the literal "Invalid Date" in the UI. They render the actual runtime shape so
// that regression cannot come back undetected.

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ user: { id: "user-1" }, organizationId: "org-1" }),
}));

const useAuditLogMock = vi.fn();
vi.mock("@/features/audit/service/queries", () => ({
	useAuditLog: (...args: unknown[]) => useAuditLogMock(...args),
}));

import {
	ACTION_ICONS,
	ACTION_PHRASES,
	ActivityFeed,
} from "@/components/activity-feed";

function event(createdAt: string | undefined) {
	return {
		id: "e1",
		eventType: "saas.auth.login",
		actorId: "user-1",
		resource: "session",
		resourceId: "s1",
		createdAt,
	};
}

afterEach(() => {
	cleanup();
	useAuditLogMock.mockReset();
});

// The icon and phrase maps are keyed on registered audit event types. Before
// the namespace cutover they carried four names no producer could ever emit
// (billing.subscription_*, role.granted), so those rows silently fell through to
// the raw-key fallback. Pin the shape so that class of dead key cannot come back.
describe("ActivityFeed action maps", () => {
	it("keys every icon and phrase on a namespaced event type", () => {
		const namespaced = /^saas\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$/;
		for (const key of [
			...Object.keys(ACTION_ICONS),
			...Object.keys(ACTION_PHRASES),
		]) {
			expect(key).toMatch(namespaced);
		}
	});
});

describe("ActivityFeed timestamps", () => {
	it("renders a relative time from a recent ISO-8601 createdAt, never 'Invalid Date'", () => {
		const createdAt = new Date(Date.now() - 10_000).toISOString();
		useAuditLogMock.mockReturnValue({
			data: { events: [event(createdAt)] },
			isLoading: false,
		});

		render(<ActivityFeed />);

		expect(screen.queryByText("Invalid Date")).toBeNull();
		expect(screen.getByText("just now")).toBeTruthy();
	});

	it("formats an old ISO-8601 createdAt as a date, never 'Invalid Date'", () => {
		// Exercises the final branch (new Date(ms).toLocaleDateString()) with the
		// exact ISO-string input that produced "Invalid Date" before the fix.
		useAuditLogMock.mockReturnValue({
			data: { events: [event("2020-01-15T00:00:00.000Z")] },
			isLoading: false,
		});

		render(<ActivityFeed />);

		expect(screen.queryByText("Invalid Date")).toBeNull();
		expect(
			screen.getByText(
				new Date("2020-01-15T00:00:00.000Z").toLocaleDateString(),
			),
		).toBeTruthy();
	});

	it("renders the row with a blank time when createdAt is missing", () => {
		useAuditLogMock.mockReturnValue({
			data: { events: [event(undefined)] },
			isLoading: false,
		});

		render(<ActivityFeed />);

		expect(screen.queryByText("Invalid Date")).toBeNull();
		// The event line still renders even with no timestamp.
		expect(screen.getByText("signed in")).toBeTruthy();
	});
});
