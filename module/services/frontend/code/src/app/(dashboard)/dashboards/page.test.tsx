import { screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import DashboardsPage from "./page";

vi.mock("@/lib/auth", () => ({ useAuth: () => ({ organizationId: "org-1" }) }));

afterEach(() => {
	window.localStorage.clear();
});

describe("DashboardsPage", () => {
	it("mounts the runtime editor with an empty starter draft", async () => {
		server.use(
			http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
				HttpResponse.json({ types: [] }),
			),
		);

		renderInApp(<DashboardsPage />);

		expect(
			await screen.findByRole("heading", { name: "Dashboards" }),
		).toBeTruthy();
		expect(screen.getByText(/No widgets yet/)).toBeTruthy();
		expect(screen.getByRole("button", { name: "Add widget" })).toBeTruthy();
	});
});
