import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

// AuditPage reads the active tenant from the signed access token via useAuth,
// which throws outside an AuthProvider. Supply a stable org so the page can
// resolve its actor directory.
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1" }),
}));

import { AuditPage } from "./audit-page";

afterEach(cleanup);

function auditEvent(overrides: Record<string, unknown> = {}) {
	return {
		id: "evt-1",
		actorId: "a3f81c2e-0000-4000-8000-000000000001",
		actorType: "user",
		eventType: "saas.auth.login",
		category: "auth",
		resource: "session",
		resourceId: "sess-1",
		orgId: "org-1",
		payload: {},
		ipAddress: "203.0.113.7",
		...overrides,
	};
}

describe("AuditPage admin container", () => {
	it("renders the audit events the service returns", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({ events: [auditEvent()], totalCount: 1 }),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("203.0.113.7")).toBeTruthy();
		// The table humanizes the event type without its namespace segment.
		expect(screen.getByText("Auth Login")).toBeTruthy();
		expect(screen.queryByText("Saas Auth Login")).toBeNull();
	});

	it("renders an actor by display name rather than by truncated id", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({ events: [auditEvent()], totalCount: 1 }),
			),
			http.post(rpc("PrincipalService", "ListPrincipals"), () =>
				HttpResponse.json({
					principals: [
						{
							id: "a3f81c2e-0000-4000-8000-000000000001",
							displayName: "Ada Lovelace",
							kind: "PRINCIPAL_KIND_HUMAN",
						},
					],
					nextPageToken: "",
				}),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("Ada Lovelace")).toBeTruthy();
		expect(screen.queryByText("a3f81c2e...")).toBeNull();
		// actor_type stays beside the name: an agent's action must not read as
		// a person's.
		expect(screen.getByText("user")).toBeTruthy();
	});

	it("falls back to the truncated id for a principal the directory misses", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({
					events: [auditEvent({ actorType: "agent" })],
					totalCount: 1,
				}),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("a3f81c2e...")).toBeTruthy();
		expect(screen.getByText("agent")).toBeTruthy();
	});
});
