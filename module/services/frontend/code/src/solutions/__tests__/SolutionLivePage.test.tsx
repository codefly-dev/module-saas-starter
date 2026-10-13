import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
const state = vi.hoisted(() => ({
	current: [{ id: "example", assetVersion: "old", available: true }],
	loaded: true,
	router: { refresh: vi.fn() },
}));
vi.mock("next/navigation", () => ({ useRouter: () => state.router }));
vi.mock("../SolutionsMenu", () => ({
	useRegisteredSolutions: () => state.current,
	useRegisteredSolutionsLoaded: () => state.loaded,
}));
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
