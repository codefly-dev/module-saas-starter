import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { DatasourceStatus } from "@codefly-dev/saas-sdk";
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
				body: JSON.parse(
					typeof init.body === "string"
						? init.body
						: new TextDecoder().decode(init.body as Uint8Array),
				),
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
			boundaryLabel: "handbook",
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
			contentResource: "example-records",
		});
		const operations = {
			listSources: () => client.listSources("org-1"),
			listAccessibleScopes: () => client.listAccessibleScopes!("org-1"),
			listActivity: () => client.listActivity!("org-1", "ds-1"),
			addGitHubSource: () =>
				client.addGitHubSource({
					orgId: "org-1",
					repo: "acme/example",
					paths: [],
					branch: "main",
					targetCollection: "Example",
					webhookSecret: "",
				}),
			beginGitHubAppSetup: () => client.beginGitHubAppSetup!("org-1"),
			completeGitHubAppSetup: () =>
				client.completeGitHubAppSetup!("org-1", "state-1", "42", "oauth-1"),
			migrateGitHubSourceToApp: () =>
				client.migrateGitHubSourceToApp!("org-1", "ds-1"),
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
			apiBase: "/api/solutions/guides/proxy",
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
				fileExtensions: [],
				boundaryNodeId: "11111111-1111-1111-1111-111111111111",
				boundaryLabel: "handbook",
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
			"/api/solutions/guides/proxy/saas.accounts.v1.DatasourceService/ListSources",
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
			apiBase: "/api/solutions/guides/proxy",
			getAccessToken: () => "test-token",
		});

		const [view] = await client.listSources("org-1");

		expect(view.lastIngestedAt).toBeUndefined();
		expect(view.lastIngestedCommit).toBeUndefined();
	});

	it("carries a degraded source and its reason across the boundary", async () => {
		// Collapsing DEGRADED into "unknown" and dropping the reason is what made
		// the tenant-readable reason unreadable.
		const [source] = oneSource.datasources;
		stubFetch({
			datasources: [
				{
					...source,
					status: "DATASOURCE_STATUS_DEGRADED",
					statusReason:
						"snapshot manifest is 12582912 bytes, over the 8388608-byte ingest limit",
				},
			],
		});
		const client = createDatasourceClient({
			apiBase: "/api/solutions/guides/proxy",
			getAccessToken: () => "test-token",
		});

		const [view] = await client.listSources("org-1");

		expect(view.status).toBe("degraded");
		expect(view.statusReason).toBe(
			"snapshot manifest is 12582912 bytes, over the 8388608-byte ingest limit",
		);
	});

	it("maps every status the wire can carry to a named state", async () => {
		// The server pins its own switch over every stored status
		// (TestDatasourceStatusToProto_MapsEveryStoredStatus). Without the same
		// pin here, a status added to the enum and left unmapped reaches a tenant
		// as "Unknown" with no test failing — which is how DEGRADED arrived.
		const [source] = oneSource.datasources;
		const statuses = Object.values(DatasourceStatus).filter(
			(value): value is DatasourceStatus =>
				typeof value === "number" && value !== DatasourceStatus.UNSPECIFIED,
		);
		expect(statuses.length).toBeGreaterThan(0);

		for (const status of statuses) {
			stubFetch({ datasources: [{ ...source, status }] });
			const client = createDatasourceClient({
				apiBase: "/api/solutions/guides/proxy",
				getAccessToken: () => "test-token",
			});

			const [view] = await client.listSources("org-1");

			expect(view.status, `unmapped DatasourceStatus ${status}`).not.toBe(
				"unknown",
			);
		}
	});

	it("leaves the reason unset for a source that never left active", async () => {
		// The wire carries an empty string, not an absent field.
		const [source] = oneSource.datasources;
		stubFetch({ datasources: [{ ...source, statusReason: "" }] });
		const client = createDatasourceClient({
			apiBase: "/api/solutions/guides/proxy",
			getAccessToken: () => "test-token",
		});

		const [view] = await client.listSources("org-1");

		expect(view.status).toBe("active");
		expect(view.statusReason).toBeUndefined();
	});

	it("reads the current token on each request", async () => {
		const { calls } = stubFetch({ datasources: [] });
		let token = "first";
		const client = createDatasourceClient({
			apiBase: "/api/solutions/guides/proxy",
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
			apiBase: "/api/solutions/guides/proxy",
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
			apiBase: "/api/solutions/guides/proxy",
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
			apiBase: "/api/solutions/guides/proxy",
			getAccessToken: () => "stale-token",
		});

		await expect(client.listSources("org-1")).rejects.toThrow();
	});

	it("surfaces the original error when refresh yields no token", async () => {
		const client = createDatasourceClient({
			apiBase: "/api/solutions/guides/proxy",
			getAccessToken: () => "stale-token",
			refreshAccessToken: async () => null,
		});
		stubFetchSequence([unauthorized(), reply(oneSource)]);

		await expect(client.listSources("org-1")).rejects.toThrow();
	});
});

function claimsToken(claims: Record<string, unknown>): string {
	const body = btoa(JSON.stringify(claims))
		.replace(/\+/g, "-")
		.replace(/\//g, "_")
		.replace(/=+$/, "");
	return `header.${body}.signature`;
}

describe("DatasourcesPanel gateway binding", () => {
	it.each([
		["a member", { or: "member" }, false],
		["an organization admin", { or: "admin" }, true],
		["a platform super administrator", { pr: "super_admin" }, true],
	])(
		"offers %s the management controls only when their credential names the tier",
		async (_who, claims, manages) => {
			stubFetch(oneSource);
			render(
				<DatasourcesPanel
					orgId="org-1"
					gateway={{
						apiBase: "/api/solutions/guides/proxy",
						getAccessToken: () => claimsToken(claims),
					}}
				/>,
			);
			await screen.findByText("codefly-dev/module-saas-starter");
			expect(!!screen.queryByRole("button", { name: "Connect GitHub" })).toBe(
				manages,
			);
			expect(!!screen.queryByRole("button", { name: "Sync" })).toBe(manages);
			expect(!!screen.queryByRole("button", { name: /More actions for/ })).toBe(
				manages,
			);
		},
	);

	it("self-wires its own React-Query provider and renders live sources", async () => {
		stubFetch(oneSource);

		// No external QueryClientProvider — the gateway-bound panel supplies one.
		render(
			<DatasourcesPanel
				orgId="org-1"
				gateway={{
					apiBase: "/api/solutions/guides/proxy",
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
					apiBase: "/api/solutions/guides/proxy",
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
	const client = createDatasourceClient({
		apiBase: "http://example.test",
		getAccessToken: () => "viewer",
	});
	await expect(
		client.syncSource("org-1", "source-1", "test-only-replacement"),
	).resolves.toBe("job-1");
	await client.syncSource("org-1", "source-1");
	expect(calls[0].url).toContain(
		"saas.accounts.v1.DatasourceService/SyncSource",
	);
	expect(calls[0].body).toEqual({
		orgId: "org-1",
		id: "source-1",
		accessToken: "test-only-replacement",
	});
	expect(calls[1].body).toEqual({ orgId: "org-1", id: "source-1" });
});

it("connects to the selected node without deriving authority from its label", async () => {
	const { calls } = stubFetch({});
	const client = createDatasourceClient({
		apiBase: "/api/solutions/example/proxy",
		getAccessToken: () => "test-token",
	});
	await client.addGitHubSource({
		orgId: "org-1",
		repo: "acme/example",
		paths: [],
		branch: "main",
		targetCollection: "Example Collection",
		boundaryNodeId: "11111111-1111-1111-1111-111111111111",
		accessToken: "test-pat",
		webhookSecret: "",
	});
	expect(calls[0].body).toMatchObject({
		boundaryNodeId: "11111111-1111-1111-1111-111111111111",
	});
	expect(calls[0].body).not.toHaveProperty("collectionLabel");
});

it("carries a source file filter through the generated SDK", async () => {
	const { calls } = stubFetch({});
	const client = createDatasourceClient({
		apiBase: "/api/solutions/example/proxy",
		getAccessToken: () => "test-token",
	});
	await client.addGitHubSource({
		orgId: "org-1",
		repo: "acme/example",
		paths: ["docs/"],
		fileExtensions: [".md", ".mdx"],
		branch: "main",
		targetCollection: "Example Collection",
		accessToken: "test-pat",
		webhookSecret: "",
	});
	expect(calls[0].body).toMatchObject({
		paths: ["docs/"],
		fileExtensions: [".md", ".mdx"],
	});
});

it("cannot answer the scope question when the composition declares no content resource", async () => {
	// An empty scope list is a verdict — the panel renders it as the viewer
	// holding no read access — and an undeclared composition authorised nobody to
	// state one. Rejecting is what leaves the answer unresolved instead. The
	// method stays present: consumers of the published kit call it through the
	// optional-property `!`, so removing it would crash them rather than answer.
	const { calls } = stubFetch({});
	const client = createDatasourceClient({
		apiBase: "/api/solutions/example/proxy",
		getAccessToken: () => "test-token",
	});
	expect(client.listAccessibleScopes).toBeTypeOf("function");
	await expect(client.listAccessibleScopes!("org-1")).rejects.toThrow(
		/no collection content resource is declared/,
	);
	expect(calls).toHaveLength(0);
});

it("never tells an undeclared deployment's viewer they were refused", async () => {
	// End to end over the real gateway client: the panel must not render the
	// "No readable collection" verdict, nor "No access" on a source row, from a
	// lookup that never happened.
	const { calls } = stubFetch(oneSource);

	render(
		<DatasourcesPanel
			orgId="org-1"
			gateway={{
				apiBase: "/api/solutions/example/proxy",
				getAccessToken: () => "test-token",
			}}
		/>,
	);

	expect(
		await screen.findByText("codefly-dev/module-saas-starter"),
	).toBeTruthy();
	await waitFor(() =>
		expect(screen.getByText(/Read permission unresolved/)).toBeTruthy(),
	);
	expect(screen.queryByText(/No readable collection/)).toBeNull();
	expect(screen.queryByText("No access")).toBeNull();
	expect(
		calls.some((call) => call.url.includes("ListMyAccessibleScopes")),
	).toBe(false);
});

it("asks the permission service for the resource the composition declared", async () => {
	const { calls } = stubFetch({});
	const client = createDatasourceClient({
		apiBase: "/api/solutions/example/proxy",
		getAccessToken: () => "test-token",
		contentResource: "example-records",
	});
	await client.listAccessibleScopes!("org-1");
	expect(calls[0].body).toMatchObject({
		resourceType: "example-records",
		action: "read",
	});
});

it("reports which scopes the viewer reads only as a platform administrator", async () => {
	stubFetch({
		scopes: [
			{ nodeId: "granted", label: "Granted", kind: "collection", basis: "ACCESS_BASIS_GRANT" },
			{
				nodeId: "platform",
				label: "Platform",
				kind: "collection",
				basis: "ACCESS_BASIS_PLATFORM_ADMINISTRATOR",
			},
			// A server that predates the basis reports none: that is not platform authority.
			{ nodeId: "unstated", label: "Unstated", kind: "collection" },
		],
	});
	const client = createDatasourceClient({
		apiBase: "/api/solutions/example/proxy",
		getAccessToken: () => "test-token",
		contentResource: "example-records",
	});
	const scopes = await client.listAccessibleScopes!("org-1");
	expect(
		Object.fromEntries(scopes.map((scope) => [scope.nodeId, scope.viaPlatformAdministrator])),
	).toEqual({ granted: false, platform: true, unstated: false });
});
