import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { server } from "@/test/setup";
import { AuditPage } from "./audit-page";

function rpc(service: string, method: string) {
	return `http://localhost:3000/saas.accounts.v1.${service}/${method}`;
}
function renderInApp(ui: React.ReactElement) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>{ui}</QueryClientProvider>,
	);
}
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
