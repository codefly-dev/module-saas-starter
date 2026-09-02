import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import DashboardsPage from "./page";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1", user: { id: "u-1" } }),
}));

afterEach(() => {
	window.localStorage.clear();
});

describe("DashboardsPage", () => {
	it("mounts the My Dashboards surface with an empty collection", async () => {
		renderInApp(<DashboardsPage />);

		expect(
			await screen.findByRole("heading", { name: "Dashboards" }),
		).toBeTruthy();
		expect(screen.getByText(/No dashboards yet/)).toBeTruthy();
		expect(screen.getByRole("button", { name: /Create/ })).toBeTruthy();
	});
});
