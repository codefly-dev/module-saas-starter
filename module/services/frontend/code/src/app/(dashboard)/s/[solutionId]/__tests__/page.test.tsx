import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const { findSolution } = vi.hoisted(() => ({ findSolution: vi.fn() }));
vi.mock("@/solutions/registry", () => ({
	findSolution,
	browserManifestUrl: () => "/assets/mf-manifest.json",
	solutionProxyBase: (id: string) => `/api/solutions/${id}/proxy`,
}));
// The remote and the dashboard are their own components with their own tests;
// what is pinned here is where the page puts them.
vi.mock("@/solutions/SolutionRuntime", () => ({
	SolutionRuntime: ({ remote }: { remote: { id: string } }) => <div data-testid="remote">{remote.id}</div>,
}));
vi.mock("@/solutions/SolutionDashboard", () => ({
	SolutionDashboards: () => <div data-testid="dashboard">dashboards</div>,
}));

import SolutionPage from "../page";

afterEach(cleanup);

const solution = (dashboard?: object) => ({
	id: "example-solution",
	nav: { title: "Example" },
	frontend: { exposedModule: "./Page" },
	...(dashboard ? { dashboard } : {}),
});

async function renderPage() {
	render(await SolutionPage({ params: Promise.resolve({ solutionId: "example-solution" }) }));
}

describe("the solution page", () => {
	// The declared dashboard was stacked above the solution, so a dashboard with
	// nothing to show yet sat on top of what the person came for.
	it("puts a declared dashboard in its own tab beside the solution, not above it", async () => {
		findSolution.mockResolvedValue(solution({ events: [], metrics: [], dashboards: [] }));
		await renderPage();
		expect(screen.getByRole("tab", { name: "App" })).toBeTruthy();
		expect(screen.getByRole("tab", { name: "Dashboard" })).toBeTruthy();
		expect(screen.getByTestId("remote")).toBeTruthy();
		expect(screen.queryByTestId("dashboard")).toBeNull();

		fireEvent.click(screen.getByRole("tab", { name: "Dashboard" }));
		expect(await screen.findByTestId("dashboard")).toBeTruthy();
		// The solution stays mounted behind the dashboard, so its state survives.
		expect(screen.getByTestId("remote")).toBeTruthy();
	});

	it("shows a solution without a dashboard as before, with no tab bar", async () => {
		findSolution.mockResolvedValue(solution());
		await renderPage();
		expect(screen.getByTestId("remote")).toBeTruthy();
		expect(screen.queryByRole("tablist")).toBeNull();
	});
});
