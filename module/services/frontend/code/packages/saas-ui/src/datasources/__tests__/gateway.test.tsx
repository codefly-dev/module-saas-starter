import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DatasourcesPanel } from "../datasources-panel.js";
import { createDatasourceClient } from "../gateway.js";

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
});

interface FetchReply {
	status: number;
	body: unknown;
}

function reply(body: unknown, status = 200): FetchReply {
	return { status, body };
}

// A Connect unary 401 — the shape the gateway returns when the bearer token has
// expired or is missing. connect-web reads `code` and raises Code.Unauthenticated.
function unauthorized(): FetchReply {
	return {
		status: 401,
		body: { code: "unauthenticated", message: "token expired" },
	};
}

interface FetchCall {
	url: string;
	/** Snapshotted at call time — the interceptor mutates req.header in place on retry. */
	authorization: string | null;
}

function stubFetchSequence(replies: FetchReply[]): { calls: FetchCall[] } {
	const calls: FetchCall[] = [];
	let i = 0;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
			calls.push({
				url: String(input),
				authorization: new Headers(init.headers).get("authorization"),
			});
			const r = replies[Math.min(i, replies.length - 1)];
			i += 1;
			return new Response(JSON.stringify(r.body), {
				status: r.status,
				headers: { "content-type": "application/json" },
			});
		}),
	);
	return { calls };
}

function stubFetch(responseBody: unknown): { calls: FetchCall[] } {
	return stubFetchSequence([reply(responseBody)]);
}

const oneSource = {
	datasources: [
		{
			id: "ds-1",
			orgId: "org-1",
			provider: "DATASOURCE_PROVIDER_GITHUB",
			github: { repo: "codefly-dev/module-saas-starter", paths: ["docs/"] },
			targetCollection: "docs",
			status: "DATASOURCE_STATUS_ACTIVE",
			webhookConfigured: true,
		},
	],
};

describe("createDatasourceClient", () => {
	it("calls the live DatasourceService through the gateway with the host token", async () => {
		const { calls } = stubFetch(oneSource);
		const client = createDatasourceClient({
			apiBase: "/api/solutions/wiki/proxy",
			getAccessToken: () => "test-token",
		});

		const sources = await client.listSources("org-1");

		expect(sources).toEqual([
			{
				id: "ds-1",
				orgId: "org-1",
				provider: "github",
				repo: "codefly-dev/module-saas-starter",
				paths: ["docs/"],
				branch: "",
				targetCollection: "docs",
				webhookConfigured: true,
				status: "active",
				lastSyncedAt: undefined,
				createdAt: undefined,
			},
		]);
		expect(calls).toHaveLength(1);
		expect(calls[0].url).toContain(
			"/api/solutions/wiki/proxy/saas.accounts.v1.DatasourceService/ListSources",
		);
		expect(calls[0].authorization).toBe("Bearer test-token");
	});

	it("reads the current token on each request", async () => {
		const { calls } = stubFetch({ datasources: [] });
		let token = "first";
		const client = createDatasourceClient({
			apiBase: "/api/solutions/wiki/proxy",
			getAccessToken: () => token,
		});

		await client.listSources("org-1");
		token = "second";
		await client.listSources("org-1");

		expect(calls[0].authorization).toBe("Bearer first");
		expect(calls[1].authorization).toBe("Bearer second");
	});

	it("refreshes and retries once when a request comes back Unauthenticated", async () => {
		const { calls } = stubFetchSequence([unauthorized(), reply(oneSource)]);
		const refreshAccessToken = vi.fn(async () => "fresh-token");
		const client = createDatasourceClient({
			apiBase: "/api/solutions/wiki/proxy",
			getAccessToken: () => "stale-token",
			refreshAccessToken,
		});

		const sources = await client.listSources("org-1");

		expect(sources).toHaveLength(1);
		expect(refreshAccessToken).toHaveBeenCalledTimes(1);
		expect(calls).toHaveLength(2);
		// First attempt carried the stale token; the retry carried the fresh one.
		expect(calls[0].authorization).toBe("Bearer stale-token");
		expect(calls[1].authorization).toBe("Bearer fresh-token");
	});

	it("recovers an initial 401 when no token was installed yet", async () => {
		const { calls } = stubFetchSequence([unauthorized(), reply(oneSource)]);
		const client = createDatasourceClient({
			apiBase: "/api/solutions/wiki/proxy",
			getAccessToken: () => null,
			refreshAccessToken: async () => "fresh-token",
		});

		await expect(client.listSources("org-1")).resolves.toHaveLength(1);
		expect(calls[0].authorization).toBeNull();
		expect(calls[1].authorization).toBe("Bearer fresh-token");
	});

	it("does not retry a 401 without a refresh capability", async () => {
		stubFetchSequence([unauthorized()]);
		const client = createDatasourceClient({
			apiBase: "/api/solutions/wiki/proxy",
			getAccessToken: () => "stale-token",
		});

		await expect(client.listSources("org-1")).rejects.toThrow();
	});

	it("surfaces the original error when refresh yields no token", async () => {
		const client = createDatasourceClient({
			apiBase: "/api/solutions/wiki/proxy",
			getAccessToken: () => "stale-token",
			refreshAccessToken: async () => null,
		});
		stubFetchSequence([unauthorized(), reply(oneSource)]);

		await expect(client.listSources("org-1")).rejects.toThrow();
	});
});

describe("DatasourcesPanel gateway binding", () => {
	it("self-wires its own React-Query provider and renders live sources", async () => {
		stubFetch(oneSource);

		// No external QueryClientProvider — the gateway-bound panel supplies one.
		render(
			<DatasourcesPanel
				orgId="org-1"
				gateway={{
					apiBase: "/api/solutions/wiki/proxy",
					getAccessToken: () => "test-token",
				}}
			/>,
		);

		expect(
			await screen.findByText("codefly-dev/module-saas-starter"),
		).toBeTruthy();
	});

	it("shows the empty state when the gateway returns no sources", async () => {
		stubFetch({ datasources: [] });

		render(
			<DatasourcesPanel
				orgId="org-1"
				gateway={{
					apiBase: "/api/solutions/wiki/proxy",
					getAccessToken: () => null,
				}}
			/>,
		);

		await waitFor(() =>
			expect(screen.getByText(/no data sources connected/i)).toBeTruthy(),
		);
	});
});
