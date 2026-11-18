import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DashboardAuthoring } from "@/features/dashboard";

// The installation reaches the page from the host's own projection route, which
// resolves it from the gateway-verified viewer. The outlet only relays it.
const { authedFetch } = vi.hoisted(() => ({
	authedFetch:
		vi.fn<
			(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>
		>(),
}));
vi.mock("@/lib/connect/token-store", () => ({
	authedFetch,
	getToken: () => "token",
	refreshToken: async () => "token",
	subscribeToken: () => () => {},
}));
type Placement = {
	installationId?: string;
	organizationId?: string;
	viewerKey?: string;
	reach?: Record<string, { installationId: string }>;
};
const Page = ({
	installationId,
	organizationId,
	viewerKey,
	reach,
}: Placement) => (
	<>
		<p>installation: {installationId ?? "none"}</p>
		<p>placement: {JSON.stringify({ organizationId, viewerKey, reach })}</p>
	</>
);
vi.mock("@module-federation/runtime", () => ({
	createInstance: () => ({
		registerRemotes: vi.fn(),
		loadRemote: () => Promise.resolve({ default: Page }),
	}),
}));

import {
	type PageKind,
	placementRefusal,
	SolutionOutlet,
} from "../SolutionOutlet";

let seq = 0;
function renderOutlet(kind?: PageKind) {
	seq += 1;
	const remote = {
		id: `inst-${seq}`,
		manifestUrl: `https://inst-${seq}.example/mf-manifest.json`,
		exposedModule: "./Page",
		kind,
	};
	render(
		<SolutionOutlet
			remote={remote}
			pageProps={{ solutionId: remote.id, apiBase: "/v1" }}
			authoring={{} as DashboardAuthoring}
		/>,
	);
	return remote.id;
}

afterEach(() => {
	cleanup();
	authedFetch.mockReset();
});

describe("SolutionOutlet installation", () => {
	it("hands the page the installation the host resolved for this solution", async () => {
		authedFetch.mockResolvedValue(
			Response.json({ installationId: "inst-acme", healthy: true }),
		);
		const id = renderOutlet();
		await waitFor(() =>
			expect(screen.getByText("installation: inst-acme")).toBeTruthy(),
		);
		expect(String(authedFetch.mock.calls[0]![0])).toBe(
			`/api/solutions/${id}/installation`,
		);
	});

	it("hands the page the organization, the viewer's key and the reach the host resolved", async () => {
		authedFetch.mockResolvedValue(
			Response.json({
				installationId: "inst-page",
				healthy: true,
				organizationId: "org-acme",
				viewerKey: "viewer-1",
				reach: {
					records: { installationId: "inst-records" },
					documents: { installationId: "" },
				},
			}),
		);
		renderOutlet();
		await waitFor(() =>
			expect(screen.getByText("installation: inst-page")).toBeTruthy(),
		);
		expect(screen.getByText(/^placement:/).textContent).toBe(
			`placement: ${JSON.stringify({ organizationId: "org-acme", viewerKey: "viewer-1", reach: { records: { installationId: "inst-records" } } })}`,
		);
	});

	// A refusal used to be swallowed: the page mounted with no installation and
	// could only guess why. The host knows why, so it says so and mounts nothing.
	it("says why the host refused a placement, and mounts no page with none", async () => {
		authedFetch.mockResolvedValue(
			Response.json({ error: "not_installed" }, { status: 404 }),
		);
		renderOutlet("module");
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain(
			"This module page cannot open for you.",
		);
		expect(alert.textContent).toContain(
			"You don't have access to this module page. Ask your organization administrator for access.",
		);
		expect(screen.queryByText(/^installation:/)).toBeNull();
		expect(screen.queryByText(/^placement:/)).toBeNull();
	});

	it("names a solution as a solution in the same refusal", async () => {
		authedFetch.mockResolvedValue(
			Response.json({ error: "authority_unavailable" }, { status: 503 }),
		);
		renderOutlet();
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("This solution cannot open for you.");
		expect(alert.textContent).toContain(
			"This solution is temporarily unavailable. Try again shortly.",
		);
	});

	it("refuses an answer that names no installation rather than mounting on it", async () => {
		authedFetch.mockResolvedValue(
			Response.json({ organizationId: "org-acme" }),
		);
		renderOutlet();
		expect((await screen.findByRole("alert")).textContent).toContain(
			"This page is not ready. Ask your organization administrator to check its setup.",
		);
		expect(screen.queryByText(/^installation:/)).toBeNull();
	});

	it("says a request that never reached the host, and asks again on Try again", async () => {
		authedFetch.mockRejectedValueOnce(new Error("Failed to fetch"));
		authedFetch.mockResolvedValue(
			Response.json({ installationId: "inst-back" }),
		);
		renderOutlet();
		expect((await screen.findByRole("alert")).textContent).toContain(
			"This solution is temporarily unavailable. Try again shortly.",
		);
		fireEvent.click(screen.getByRole("button", { name: "Retry" }));
		await waitFor(() =>
			expect(screen.getByText("installation: inst-back")).toBeTruthy(),
		);
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("offers no retry for missing access and hides server details", async () => {
		authedFetch.mockResolvedValue(
			Response.json({ error: "private-internal" }, { status: 403 }),
		);
		renderOutlet();
		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("Ask your organization administrator");
		expect(alert.textContent).not.toContain("private-internal");
		expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
	});

	it.each([
		[
			401,
			"unauthenticated",
			"Your sign-in has expired. Sign in again to continue.",
		],
		[403, "no_organization", "Choose an organization to open this page."],
		[
			403,
			"forbidden",
			"You don't have access to this solution. Ask your organization administrator for access.",
		],
		[429, "rate_limited", "Too many requests. Wait a moment, then try again."],
		[
			503,
			"registry_unavailable",
			"This solution is temporarily unavailable. Try again shortly.",
		],
		[500, "", "This solution is temporarily unavailable. Try again shortly."],
	])(
		"words every refusal the route can answer: %i %s",
		(status, code, said) => {
			expect(placementRefusal("solution", status, code)).toBe(said);
		},
	);
});
