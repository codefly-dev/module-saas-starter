import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DatasourcesPanel } from "../datasources-panel.js";
import { createDatasourceClient } from "../gateway.js";

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
});

function stubFetch(
	responseBody: unknown,
): { calls: Array<{ url: string; init: RequestInit }> } {
	const calls: Array<{ url: string; init: RequestInit }> = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
			calls.push({ url: String(input), init });
			return new Response(JSON.stringify(responseBody), {
				status: 200,
				headers: { "content-type": "application/json" },
			});
		}),
	);
	return { calls };
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
		expect(new Headers(calls[0].init.headers).get("authorization")).toBe(
			"Bearer test-token",
		);
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

		expect(new Headers(calls[0].init.headers).get("authorization")).toBe(
			"Bearer first",
		);
		expect(new Headers(calls[1].init.headers).get("authorization")).toBe(
			"Bearer second",
		);
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
