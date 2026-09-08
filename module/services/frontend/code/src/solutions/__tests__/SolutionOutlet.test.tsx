import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DashboardAuthoring } from "@/features/dashboard";

// The Module Federation runtime is mocked so each test drives loadRemote's
// outcome (reject transient / reject deterministic / resolve). registerRemotes
// is a no-op spy; createInstance returns one stable fake host (SolutionOutlet
// caches the host instance, so the fake must be reusable across renders).
let loadRemoteImpl: (moduleKey: string) => Promise<unknown>;
const registerRemotes = vi.fn();
const loadRemote = vi.fn((moduleKey: string) => loadRemoteImpl(moduleKey));

vi.mock("@module-federation/runtime", () => ({
	createInstance: () => ({ registerRemotes, loadRemote }),
}));

import { SolutionOutlet, type SolutionRemote } from "../SolutionOutlet";

const authoring = {} as DashboardAuthoring;

function renderOutlet(remote: SolutionRemote) {
	return render(
		<SolutionOutlet
			remote={remote}
			pageProps={{ solutionId: remote.id, apiBase: "/v1" }}
			authoring={authoring}
		/>,
	);
}

// A fresh id per test so the module-scoped remoteComponents cache (which the
// eviction fix mutates) never leaks a component between tests.
let seq = 0;
function freshRemote(): SolutionRemote {
	seq += 1;
	return {
		id: `sol-${seq}`,
		manifestUrl: `https://remote-${seq}.example/mf-manifest.json`,
		exposedModule: "./Page",
	};
}

beforeEach(() => {
	registerRemotes.mockClear();
	loadRemote.mockClear();
});

afterEach(() => {
	cleanup();
	vi.useRealTimers();
});

describe("SolutionOutlet remote loading", () => {
	it("does NOT retry a deterministic failure — loads once, then surfaces it", async () => {
		// A remote that exposes no default (or whose module throws) fails
		// identically on every attempt. Retrying it 6× just strands the user on the
		// spinner for the whole backoff budget. It must fail on the first attempt.
		loadRemoteImpl = () =>
			Promise.reject(new Error(`solution remote exposed no default`));

		renderOutlet(freshRemote());

		await waitFor(() =>
			expect(screen.getByText("This solution failed to load.")).toBeTruthy(),
		);
		expect(loadRemote).toHaveBeenCalledTimes(1);
	});

	it("evicts the failed component so a remount re-attempts the load", async () => {
		// React.lazy caches a factory rejection for the life of the component. If
		// the module-scoped cache keeps that failed lazy, the solution is pinned to
		// its error boundary for the life of the process even after the backend
		// recovers. On final failure the cache entry is evicted, so a remount builds
		// a fresh lazy and calls loadRemote again.
		const remote = freshRemote();
		loadRemoteImpl = () => Promise.reject(new Error("exposed no default"));

		const first = renderOutlet(remote);
		await waitFor(() =>
			expect(screen.getByText("This solution failed to load.")).toBeTruthy(),
		);
		expect(loadRemote).toHaveBeenCalledTimes(1);
		first.unmount();

		// Backend is healthy on the remount.
		const Page = () => <div>solution ready</div>;
		loadRemoteImpl = () => Promise.resolve({ default: Page });

		renderOutlet(remote);
		await waitFor(() =>
			expect(screen.getByText("solution ready")).toBeTruthy(),
		);
		expect(loadRemote).toHaveBeenCalledTimes(2);
	});

	it("retries a transient fetch failure and then renders the remote", async () => {
		// The cold-start case the retry exists for: the first manifest fetch loses
		// the race ("Failed to fetch"), the next wins.
		const Page = () => <div>warmed up</div>;
		let attempt = 0;
		loadRemoteImpl = () => {
			attempt += 1;
			return attempt === 1
				? Promise.reject(new Error("Failed to fetch"))
				: Promise.resolve({ default: Page });
		};

		renderOutlet(freshRemote());

		await waitFor(
			() => expect(screen.getByText("warmed up")).toBeTruthy(),
			{ timeout: 2000 },
		);
		expect(loadRemote).toHaveBeenCalledTimes(2);
		// Once at initial registration, once more between the two attempts to force
		// MF to drop its cached failed manifest snapshot and re-fetch.
		expect(registerRemotes).toHaveBeenCalledTimes(2);
	});
});
