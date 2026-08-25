import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { AuditPage } from "./audit-page";

afterEach(cleanup);

describe("AuditPage admin container", () => {
	it("renders the audit events the service returns", async () => {
		server.use(
			http.post(rpc("AuditService", "QueryAuditLog"), () =>
				HttpResponse.json({
					events: [
						{
							id: "evt-1",
							actorId: "actor-1",
							actorType: "user",
							eventType: "user.login",
							category: "auth",
							resource: "session",
							resourceId: "sess-1",
							orgId: "org-1",
							payload: {},
							ipAddress: "203.0.113.7",
						},
					],
					totalCount: 1,
				}),
			),
		);
		renderInApp(<AuditPage />);
		expect(await screen.findByText("203.0.113.7")).toBeTruthy();
	});
});
