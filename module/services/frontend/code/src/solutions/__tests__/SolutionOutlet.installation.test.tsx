import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DashboardAuthoring } from "@/features/dashboard";

// The installation reaches the page from the host's own projection route, which
// resolves it from the gateway-verified viewer. The outlet only relays it.
const { authedFetch } = vi.hoisted(() => ({ authedFetch: vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>() }));
vi.mock("@/lib/connect/token-store", () => ({
	authedFetch,
	getToken: () => "token",
	refreshToken: async () => "token",
	subscribeToken: () => () => {},
}));
const Page = ({ installationId }: { installationId?: string }) => <p>installation: {installationId ?? "none"}</p>;
vi.mock("@module-federation/runtime", () => ({
	createInstance: () => ({ registerRemotes: vi.fn(), loadRemote: () => Promise.resolve({ default: Page }) }),
}));

import { SolutionOutlet } from "../SolutionOutlet";

let seq = 0;
function renderOutlet() {
	seq += 1;
	const remote = { id: `inst-${seq}`, manifestUrl: `https://inst-${seq}.example/mf-manifest.json`, exposedModule: "./Page" };
	render(<SolutionOutlet remote={remote} pageProps={{ solutionId: remote.id, apiBase: "/v1" }} authoring={{} as DashboardAuthoring} />);
	return remote.id;
}

afterEach(() => { cleanup(); authedFetch.mockReset(); });

describe("SolutionOutlet installation", () => {
	it("hands the page the installation the host resolved for this solution", async () => {
		authedFetch.mockResolvedValue(Response.json({ installationId: "inst-acme", healthy: true }));
		const id = renderOutlet();
		await waitFor(() => expect(screen.getByText("installation: inst-acme")).toBeTruthy());
		expect(String(authedFetch.mock.calls[0]![0])).toBe(`/api/solutions/${id}/installation`);
	});

	it("hands none when the host refuses, never a value of its own", async () => {
		authedFetch.mockResolvedValue(Response.json({ error: "not_installed" }, { status: 404 }));
		renderOutlet();
		await waitFor(() => expect(screen.getByText("installation: none")).toBeTruthy());
		await waitFor(() => expect(authedFetch).toHaveBeenCalledTimes(1));
		expect(screen.queryByText(/installation: inst/)).toBeNull();
	});
});
