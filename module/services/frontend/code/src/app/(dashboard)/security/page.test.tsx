import { screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import SecurityPage from "./page";

vi.mock("@/lib/auth", () => ({ useAuth: () => ({ organizationId: "org-1" }) }));

describe("SecurityPage", () => {
	it("renders the second reference dashboard from its declaration", async () => {
		server.use(
			http.post(
				rpc("AuditService", "AggregateAuditLog"),
				async ({ request }) => {
					const body = (await request.json()) as { groupBy?: string };
					const buckets =
						body.groupBy === "category"
							? [
									{ key: "security", count: "12" },
									{ key: "billing", count: "7" },
								]
							: [
									{ key: "2026-08-03", count: "3" },
									{ key: "2026-08-10", count: "5" },
								];
					return HttpResponse.json({ buckets });
				},
			),
		);

		renderInApp(<SecurityPage />);

		// Category-grouped bar: humanized category label + its count — the
		// dimension /insights never exercises.
		expect(await screen.findByText("Billing")).toBeTruthy();
		expect(screen.getByText("12")).toBeTruthy();
		// Stat metric over time buckets: the summed total (3 + 5).
		expect(screen.getByText("8")).toBeTruthy();
		expect(screen.queryByText("No events yet.")).toBeNull();
	});
});
