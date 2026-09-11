import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DatasourcesPanel } from "../datasources-panel.js";
import { createDatasourceClient } from "../gateway.js";

const gatewayCatalog = JSON.parse(
	readFileSync(
		resolve(
			__dirname,
			"../../../../../../../accounts/generated/gateway-routes.json",
		),
		"utf8",
	),
);
const gatewayProcedures = new Set<string>(
	gatewayCatalog.routes
		.filter(
			(route: { protocol: string; method: string; match: string }) =>
				route.protocol === "GATEWAY_PROTOCOL_CONNECT" &&
				route.method === "POST" &&
				route.match === "GATEWAY_MATCH_EXACT",
		)
		.map((route: { path: string }) => route.path),
);

function expectRoutable(url: string) {
	const procedure = url.slice(url.lastIndexOf("/saas.accounts.v1."));
	expect(
		gatewayProcedures.has(procedure),
		`Gateway has no POST route for ${procedure}`,
	).toBe(true);
}

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
	body: unknown;
}

function stubFetchSequence(replies: FetchReply[]): { calls: FetchCall[] } {
	const calls: FetchCall[] = [];
	let i = 0;
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
			expect(init.method).toBe("POST");
			expectRoutable(String(input));
			calls.push({
				url: String(input),
				body: JSON.parse(typeof init.body === "string" ? init.body : new TextDecoder().decode(init.body as Uint8Array)),
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
			boundaryNodeId: "11111111-1111-1111-1111-111111111111",
			status: "DATASOURCE_STATUS_ACTIVE",
			webhookConfigured: true,
			lastIngestedAt: "2026-09-08T11:30:00Z",
			lastIngestedCommit: "9f2c1ab7d4e5f60718293a4b5c6d7e8f90a1b2c3",
		},
	],
};

describe("createDatasourceClient", () => {
	it("rejects missing and internal procedures in the route gate", () => {
		expect(() =>
			expectRoutable("/saas.accounts.v1.ScopeService/ListAccessibleScopes"),
		).toThrow();
		expect(() =>
			expectRoutable(
				"/saas.accounts.v1.PermissionService/ListAccessibleScopes",
			),
		).toThrow();
		expectRoutable(
			"/saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes",
		);
	});

	it("routes every datasource operation emitted by the SDK", async () => {
		const { calls } = stubFetch({});
		const client = createDatasourceClient({
			apiBase: "/api/solutions/example/proxy",
			getAccessToken: () => "test-token",
		});
		const operations = {
			listSources: () => client.listSources("org-1"),
			listActivity: () => client.listActivity!("org-1", "ds-1"),
			addGitHubSource: () =>
				client.addGitHubSource({
					orgId: "org-1",
					repo: "acme/example",
					paths: [],
					branch: "main",
					targetCollection: "Example",
					accessToken: "",
					webhookSecret: "",
				}),
			syncSource: () => client.syncSource("org-1", "ds-1"),
			deleteSource: () => client.deleteSource("org-1", "ds-1"),
		} satisfies Partial<Record<keyof typeof client, () => Promise<unknown>>>;
		expect(Object.keys(operations).sort()).toEqual(Object.keys(client).sort());
		for (const operation of Object.values(operations)) {
			const before = calls.length;
			await operation();
			expect(calls.length).toBeGreaterThan(before);
		}
	});

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
				boundaryNodeId: "11111111-1111-1111-1111-111111111111",
				webhookConfigured: true,
				status: "active",
				lastSyncedAt: undefined,
				lastIngestedAt: "2026-09-08T11:30:00.000Z",
				lastIngestedCommit: "9f2c1ab7d4e5f60718293a4b5c6d7e8f90a1b2c3",
				createdAt: undefined,
			},
		]);
		expect(calls).toHaveLength(1);
		expect(calls[0].url).toContain(
			"/api/solutions/wiki/proxy/saas.accounts.v1.DatasourceService/ListSources",
		);
		expect(calls[0].authorization).toBe("Bearer test-token");
	});

	it("leaves the ingest provenance unset before the first delivery lands", async () => {
		// The wire carries an empty string, not an absent field, for a commit that
		// has never been set; the view must not render it as a real commit.
		const [source] = oneSource.datasources;
		stubFetch({
			datasources: [
				{ ...source, lastIngestedAt: undefined, lastIngestedCommit: "" },
			],
		});
		const client = createDatasourceClient({
			apiBase: "/api/solutions/wiki/proxy",
			getAccessToken: () => "test-token",
		});

		const [view] = await client.listSources("org-1");

		expect(view.lastIngestedAt).toBeUndefined();
		expect(view.lastIngestedCommit).toBeUndefined();
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

it("exposes GitHub dispatch time as ingest provenance", async () => {
	const github = {
		...oneSource.datasources[0],
		lastIngestedAt: "2026-09-11T14:00:00Z",
	};
	stubFetch({ datasources: [github] });
	const client = createDatasourceClient({
		apiBase: "http://example.test",
		getAccessToken: () => "viewer",
	});
	const source = (await client.listSources("org-1"))[0];
	expect(source.lastSyncedAt).toBeUndefined();
	expect(source.lastIngestedAt).toBe("2026-09-11T14:00:00.000Z");
});
it("reads source-specific typed audit history", async () => {
	stubFetch({
		events: [
			{
				id: "a1",
				eventType: "saas.datasource.sync.completed",
				actorId: "worker",
				createdAt: "2026-09-11T14:00:00Z",
				payload: { processed: 45, job_id: "j1" },
			},
		],
	});
	const client = createDatasourceClient({
		apiBase: "http://example.test",
		getAccessToken: () => "viewer",
	});
	const events = await client.listActivity!("org-1", "s1");
	expect(events[0].fields).toEqual({ processed: 45, job_id: "j1" });
});

it("serializes a replacement credential only for reconnect on the existing source", async () => {
 const { calls } = stubFetch({ jobId: "job-1" });
 const client = createDatasourceClient({ apiBase: "http://example.test", getAccessToken: () => "viewer" });
 await expect(client.syncSource("org-1", "source-1", "test-only-replacement")).resolves.toBe("job-1");
 await client.syncSource("org-1", "source-1");
 expect(calls[0].url).toContain("saas.accounts.v1.DatasourceService/SyncSource");
 expect(calls[0].body).toEqual({ orgId: "org-1", id: "source-1", accessToken: "test-only-replacement" });
 expect(calls[1].body).toEqual({ orgId: "org-1", id: "source-1" });
});
