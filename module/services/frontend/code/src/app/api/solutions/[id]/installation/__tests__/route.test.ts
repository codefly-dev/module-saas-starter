import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

const { getWorkspaceSecret, getEndpoints } = vi.hoisted(() => ({
	getWorkspaceSecret: vi.fn<(name: string, key: string) => string | undefined>(),
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(() => []),
}));
vi.mock("codefly", () => ({ getWorkspaceSecret, getEndpoints }));

import { GET } from "@/app/api/solutions/[id]/installation/route";

const GATEWAY = "http://gateway.internal:8080";

function manifest(id: string): string {
	return JSON.stringify({
		id,
		nav: { title: id.toUpperCase(), path: `/s/${id}` },
		frontend: { type: "module-federation", manifestUrl: `https://${id}.internal/mf-manifest.json`, exposedModule: "./Page" },
	});
}

/** The registry, and the gateway-verified entitlement read for the viewer. */
function gatewayServing(registered: string[], entitled: Array<{ id: string; installationId?: string; healthy?: boolean }>) {
	return vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
		const path = new URL(input instanceof Request ? input.url : String(input)).pathname;
		if (path === "/solutions/_entitlements") {
			if (!new Headers(init?.headers).get("authorization")) {
				return new Response(JSON.stringify({ error: "unauthenticated" }), { status: 401 });
			}
			return Response.json({
				org: "org-acme",
				viewer: "viewer-1",
				solutions: entitled.map((entry) => ({
					targetId: `target-${entry.id}`,
					healthy: entry.healthy ?? true,
					scopeNodeId: `node-${entry.id}`,
					...(entry.installationId === undefined ? {} : { installationId: entry.installationId }),
				})),
			});
		}
		if (path === "/solutions/_registry") {
			return Response.json({
				revision: 1,
				solutions: registered.map((id) => ({ id, status: "active", targetId: `target-${id}`, manifest: manifest(id) })),
			});
		}
		return new Response("unexpected", { status: 500 });
	});
}

function resetRegistryCache() {
	const g = globalThis as Record<string, unknown>;
	g.__solutionSnapshot = null;
	g.__solutionSnapshotInFlight = null;
	g.__solutionProjectionCache = new Map();
}

function call(id: string, credential = "Bearer viewer-token") {
	const headers = new Headers();
	if (credential) headers.set("authorization", credential);
	return GET(new Request(`http://frontend/api/solutions/${id}/installation`, { headers }), { params: Promise.resolve({ id }) });
}

describe("solution installation route", () => {
	beforeEach(() => {
		resetRegistryCache();
		getWorkspaceSecret.mockReturnValue("internal-test-token");
		getEndpoints.mockReturnValue([{ service: "auth-gateway", name: "rest", address: `${GATEWAY}/rest` }]);
	});
	afterEach(() => {
		getWorkspaceSecret.mockReset();
		vi.unstubAllGlobals();
		resetRegistryCache();
	});

	it("answers the viewer organization's installation of the routed target", async () => {
		vi.stubGlobal("fetch", gatewayServing(["solution-a", "solution-b"], [
			{ id: "solution-a", installationId: "inst-a" },
			{ id: "solution-b", installationId: "inst-b" },
		]));
		const res = await call("solution-a");
		expect(res.status).toBe(200);
		expect(await res.json()).toEqual({ installationId: "inst-a", healthy: true });
	});

	it("answers 404 for a registered solution the viewer is not entitled to", async () => {
		vi.stubGlobal("fetch", gatewayServing(["solution-a", "solution-b"], [{ id: "solution-b", installationId: "inst-b" }]));
		const res = await call("solution-a");
		expect(res.status).toBe(404);
		expect(await res.json()).not.toHaveProperty("installationId");
	});

	it("answers 404 for an unregistered solution, as for an unentitled one", async () => {
		vi.stubGlobal("fetch", gatewayServing(["solution-b"], [{ id: "solution-b", installationId: "inst-b" }]));
		expect((await call("solution-a")).status).toBe(404);
	});

	it("answers 404 when an older gateway does not say which installation", async () => {
		vi.stubGlobal("fetch", gatewayServing(["solution-a"], [{ id: "solution-a" }]));
		expect((await call("solution-a")).status).toBe(404);
	});

	it("refuses a viewer the gateway did not verify instead of answering", async () => {
		vi.stubGlobal("fetch", gatewayServing(["solution-a"], [{ id: "solution-a", installationId: "inst-a" }]));
		const res = await call("solution-a", "");
		expect(res.status).not.toBe(200);
		expect(await res.text()).not.toContain("inst-a");
	});
});
