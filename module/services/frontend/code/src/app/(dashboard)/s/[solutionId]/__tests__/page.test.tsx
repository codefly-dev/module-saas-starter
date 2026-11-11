import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const { findSolution } = vi.hoisted(() => ({ findSolution: vi.fn() }));
vi.mock("@/solutions/registry", () => ({
	findSolution,
	browserManifestUrl: () => "/assets/mf-manifest.json",
	solutionProxyBase: (id: string) => `/api/solutions/${id}/proxy`,
	solutionModuleApiBase: (id: string) => `/api/solutions/${id}/proxy/_browser`,
}));
// The remote and the dashboard are their own components with their own tests;
// what is pinned here is where the page puts them.
vi.mock("@/solutions/SolutionRuntime", () => ({
	SolutionRuntime: ({
		remote,
		pageProps,
	}: {
		remote: { id: string };
		pageProps: { moduleApiBase?: string };
	}) => (
		<div data-testid="remote" data-module-base={pageProps.moduleApiBase}>
			{remote.id}
		</div>
	),
}));
vi.mock("@/solutions/SolutionDashboard", () => ({
	SolutionDashboards: () => <div data-testid="dashboard">dashboards</div>,
}));
// The Setup UI is the kit's `<SolutionSetup>`, tested in its package; what is
// pinned here is that every solution has settings, naming its target. Whether a
// module declares roles is the authority's offer review (WhenRolesDeclared,
// tested beside it); here it stands in as `moduleRoles.declared`.
const moduleRoles = vi.hoisted(() => ({ declared: false }));
vi.mock("@/features/solution-installations", () => ({
	SolutionSetupPanel: ({ target }: { target: string }) => (
		<div data-testid="setup">{target}</div>
	),
	WhenRolesDeclared: ({
		kind,
		children,
	}: {
		kind?: string;
		children: ReactNode;
	}) => (kind === "solution" || moduleRoles.declared ? <>{children}</> : null),
}));
// The live delivery gate has its own polling and router tests. Here it stands
// in for the viewer's projection: entitled renders the page, not entitled
// renders whatever the page passed as `denied`.
const live = vi.hoisted(() => ({ entitled: true }));
vi.mock("@/solutions/SolutionLivePage", () => ({
	SolutionLivePage: ({
		children,
		denied,
	}: {
		children: ReactNode;
		denied?: ReactNode;
	}) => <>{live.entitled ? children : denied}</>,
}));
// The page wrapper refreshes the App Router when a delivered generation
// changes. These tests render the server page directly, outside Next's router
// provider, so give the client wrapper the smallest router seam it needs.
vi.mock("next/navigation", () => ({
	useRouter: () => ({ refresh: vi.fn() }),
	notFound: () => {
		throw new Error("NEXT_NOT_FOUND");
	},
}));

import SolutionNotFound from "../not-found";
import SolutionPage from "../page";

afterEach(() => {
	cleanup();
	live.entitled = true;
	moduleRoles.declared = false;
});

const solution = (
	dashboard?: object,
	kind: "module" | "solution" = "solution",
) => ({
	id: "example-solution",
	kind,
	nav: { title: "Example" },
	frontend: { exposedModule: "./Page" },
	...(dashboard ? { dashboard } : {}),
});

async function renderPage() {
	render(
		await SolutionPage({
			params: Promise.resolve({ solutionId: "example-solution" }),
		}),
	);
}

describe("the solution page", () => {
	// The declared dashboard was stacked above the solution, so a dashboard with
	// nothing to show yet sat on top of what the person came for.
	it("puts a declared dashboard in its own tab beside the solution, not above it", async () => {
		findSolution.mockResolvedValue(
			solution({
				events: [],
				metrics: [],
				dashboards: [
					{ id: "overview", title: "Overview", layout: "grid", widgets: [] },
				],
			}),
		);
		await renderPage();
		expect(await screen.findByRole("tab", { name: "Workspace" })).toBeTruthy();
		expect(await screen.findByRole("tab", { name: "Dashboard" })).toBeTruthy();
		expect(await screen.findByTestId("remote")).toBeTruthy();
		expect(screen.getByTestId("remote").getAttribute("data-module-base")).toBe(
			"/api/solutions/example-solution/proxy/_browser",
		);
		expect(screen.queryByTestId("dashboard")).toBeNull();

		fireEvent.click(screen.getByRole("tab", { name: "Dashboard" }));
		expect(await screen.findByTestId("dashboard")).toBeTruthy();
		// The solution stays mounted behind the dashboard, so its state survives.
		expect(await screen.findByTestId("remote")).toBeTruthy();
	});

	// A graph may declare events and metrics and no dashboard: a Dashboard tab
	// would hide the solution behind an empty panel.
	it("shows no Dashboard tab for a graph that declares no dashboard", async () => {
		findSolution.mockResolvedValue(
			solution({ events: [], metrics: [], dashboards: [] }),
		);
		await renderPage();
		expect(await screen.findByTestId("remote")).toBeTruthy();
		expect(screen.queryByRole("tab", { name: "Dashboard" })).toBeNull();
	});

	// A solution has settings beside its workspace: the roles it declares, what
	// each grants, and who holds them.
	it.each([
		["without a dashboard", undefined],
		[
			"with a dashboard",
			{
				events: [],
				metrics: [],
				dashboards: [
					{ id: "overview", title: "Overview", layout: "grid", widgets: [] },
				],
			},
		],
	])(
		"gives a solution %s settings for its own target, keeping the workspace mounted",
		async (_, dashboard) => {
			findSolution.mockResolvedValue(solution(dashboard));
			await renderPage();
			const remote = screen.getByTestId("remote");
			const settings = screen.getByRole("button", {
				name: "Settings for Example",
			});
			expect(screen.queryByTestId("setup")).toBeNull();
			fireEvent.click(settings);
			expect(
				await screen.findByRole("dialog", { name: "Example settings" }),
			).toBeTruthy();
			expect((await screen.findByTestId("setup")).textContent).toBe(
				"example-solution",
			);
			expect(screen.getByTestId("remote")).toBe(remote);
			fireEvent.click(screen.getByRole("button", { name: "Close" }));
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
			expect(screen.getByTestId("remote")).toBe(remote);
			if (!dashboard) expect(screen.queryByRole("tab")).toBeNull();
		},
	);

	// A module page that declares installation roles has the same settings.
	it("gives a module page that declares roles settings for its own target", async () => {
		moduleRoles.declared = true;
		findSolution.mockResolvedValue(solution(undefined, "module"));
		await renderPage();
		fireEvent.click(
			screen.getByRole("button", { name: "Settings for Example" }),
		);
		expect((await screen.findByTestId("setup")).textContent).toBe(
			"example-solution",
		);
	});

	// A module page that declares no installation roles offers no settings
	// whose every answer would be a refusal.
	it("gives a module page that declares no roles no settings", async () => {
		findSolution.mockResolvedValue(solution(undefined, "module"));
		await renderPage();
		expect(screen.queryByRole("tab")).toBeNull();
		expect(
			screen.queryByRole("button", { name: "Settings for Example" }),
		).toBeNull();
		expect(screen.getByTestId("remote")).toBeTruthy();
	});

	// A viewer the projection does not list is refused, and told so: the page
	// is named, as what it is, and nothing it mounts is rendered. It is never
	// the not-found state, which is for an address nothing is registered at.
	it.each([
		["solution", "solution"],
		["module", "module page"],
	] as const)(
		"tells a viewer the projection does not list that they are not admitted to the %s, never that it is not found",
		async (kind, noun) => {
			live.entitled = false;
			findSolution.mockResolvedValue(solution(undefined, kind));
			await renderPage();
			const refusal = screen.getByRole("alert");
			expect(refusal.textContent).toContain("You are not admitted to Example");
			expect(refusal.textContent).toContain(
				`Example is a ${noun} registered here, but you are not admitted to it`,
			);
			expect(screen.queryByText(/not found/i)).toBeNull();
			expect(screen.queryByTestId("remote")).toBeNull();
			expect(
				screen.queryByRole("button", { name: "Settings for Example" }),
			).toBeNull();
		},
	);

	// An address nothing is registered at is a real 404, and says nothing about
	// admission: there is no page to be admitted to.
	it("answers an id nothing is registered under with not found, not a refusal", async () => {
		findSolution.mockResolvedValue(null);
		await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
		render(<SolutionNotFound />);
		expect(screen.getByText("Page not found")).toBeTruthy();
		expect(screen.queryByText(/not admitted/i)).toBeNull();
	});
});
