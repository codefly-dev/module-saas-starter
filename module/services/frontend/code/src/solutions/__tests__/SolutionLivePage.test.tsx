import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
const state = vi.hoisted(() => ({
	current: [{ id: "example", assetVersion: "old", available: true }] as Array<{
		id: string;
		assetVersion?: string;
		available?: boolean;
	}>,
	loaded: true,
	router: { refresh: vi.fn() },
}));
vi.mock("next/navigation", () => ({ useRouter: () => state.router }));
vi.mock("../SolutionsMenu", () => ({
	useRegisteredSolutions: () => state.current,
	useRegisteredSolutionsLoaded: () => state.loaded,
}));
vi.mock("server-only", () => ({}));
import type { ViewerEntitlements } from "../entitlements";
import { entitledSolutions, navProjection } from "../projections";
import type { SolutionManifest } from "../registry";
import { SolutionLivePage } from "../SolutionLivePage";
beforeEach(() => {
	state.current = [{ id: "example", assetVersion: "old", available: true }];
	state.loaded = true;
	state.router.refresh.mockClear();
});
afterEach(() => {
	cleanup();
	vi.useRealTimers();
});
it("refreshes an upgraded page and removes a withdrawn page without reloading the browser", () => {
	const page = (version: string) => (
		<SolutionLivePage id="example" assetVersion={version}>
			<div>remote page</div>
		</SolutionLivePage>
	);
	const view = render(page("old"));
	expect(screen.queryByText("remote page")).not.toBeNull();
	state.current = [{ id: "example", assetVersion: "new", available: true }];
	view.rerender(page("old"));
	expect(state.router.refresh).toHaveBeenCalledTimes(1);
	expect(screen.queryByText("remote page")).toBeNull();
	view.rerender(page("new"));
	expect(screen.queryByText("remote page")).not.toBeNull();
	state.current = [];
	view.rerender(page("new"));
	expect(screen.queryByText("remote page")).toBeNull();
});

it("shows loading until the first viewer projection arrives", () => {
	state.loaded = false;
	const view = render(
		<SolutionLivePage id="example" assetVersion="old">
			remote page
		</SolutionLivePage>,
	);
	expect(screen.queryByText("Loading page…")).not.toBeNull();
	expect(screen.queryByText("remote page")).toBeNull();
	state.loaded = true;
	view.rerender(
		<SolutionLivePage id="example" assetVersion="old">
			remote page
		</SolutionLivePage>,
	);
	expect(screen.queryByText("remote page")).not.toBeNull();
});
it("retries a stale server render until the new generation arrives", () => {
	vi.useFakeTimers();
	state.current[0].assetVersion = "new";
	const page = (version: string) => (
		<SolutionLivePage id="example" assetVersion={version}>
			remote page
		</SolutionLivePage>
	);
	const view = render(page("old"));
	expect(state.router.refresh).toHaveBeenCalledTimes(1);
	vi.advanceTimersByTime(5_000);
	expect(state.router.refresh).toHaveBeenCalledTimes(2);
	view.rerender(page("new"));
	vi.advanceTimersByTime(5_000);
	expect(state.router.refresh).toHaveBeenCalledTimes(2);
});

it("does not open an unentitled sibling even when a visible solution shares its group", () => {
	state.current = [
		Object.assign(
			{ id: "entitled-sibling", assetVersion: "old", available: true },
			{
				group: { id: "operations", title: "Operations", order: 1 },
			},
		),
	];
	render(
		<SolutionLivePage id="example" assetVersion="old">
			denied page
		</SolutionLivePage>,
	);
	expect(screen.queryByText("denied page")).toBeNull();
});

// A module's page reaches a member only through what the authority answers for
// that member: the module's installation, and a grant there that reaches them.
// Two members of one organization, the same registered module record, one
// granted: the navigation projection names it for that one alone, and the page
// mounts for that one alone — the other gets the page's not-found answer.
it("gives a module page to the granted member and the not-found answer to another member of the organization", () => {
	const moduleRecord = {
		id: "ledger",
		kind: "module",
		targetId: "target-ledger",
		nav: { title: "Ledger", path: "/s/ledger" },
		assetVersion: "v1",
	} as unknown as SolutionManifest;
	const viewer = (id: string, granted: boolean): ViewerEntitlements => ({
		org: "org-acme",
		viewer: id,
		byTarget: new Map(
			granted
				? [["target-ledger", { targetId: "target-ledger", healthy: true, scopeNodeId: "node-ledger", installationId: "inst-ledger" }]]
				: [],
		),
		revision: granted ? "1" : "0",
	});
	const page = (
		<SolutionLivePage id="ledger" assetVersion="v1" denied={<p>Solution not found</p>}>
			module page
		</SolutionLivePage>
	);
	for (const [member, granted] of [["member-granted", true], ["member-other", false]] as const) {
		cleanup();
		state.current = entitledSolutions([moduleRecord], viewer(member, granted)).map(
			({ manifest, entitlement }) => navProjection(manifest, entitlement),
		);
		expect(state.current.map((entry) => entry.id)).toEqual(granted ? ["ledger"] : []);
		render(page);
		expect(screen.queryByText("module page") !== null).toBe(granted);
		expect(screen.queryByText("Solution not found") !== null).toBe(!granted);
	}
});
