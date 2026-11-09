import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

const { getWorkspaceSecret, getEndpoints } = vi.hoisted(() => ({
	getWorkspaceSecret: vi.fn<(name: string, key: string) => string | undefined>(),
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(() => []),
}));
vi.mock("codefly", async () => ({
	...(await vi.importActual<typeof import("codefly")>("codefly")), networkInstance: () => getEndpoints()[0], getWorkspaceSecret, getEndpoints }));

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
type Registered = string | { id: string; kind: "module" | "solution"; reaches?: string[]; page?: boolean };

/**
 * `composed` is the organization's composed modules without roles, which the
 * gateway answers only to a read that asks for them.
 */
function gatewayServing(
	registered: Registered[],
	entitled: Array<{ id: string; installationId?: string; healthy?: boolean }>,
	composed: Array<{ id: string; installationId: string }> = [],
) {
	return vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
		const url = new URL(input instanceof Request ? input.url : String(input));
		const path = url.pathname;
		if (path === "/solutions/_entitlements") {
			if (!new Headers(init?.headers).get("authorization")) {
				return new Response(JSON.stringify({ error: "unauthenticated" }), { status: 401 });
			}
			const asked = url.searchParams.get("composedModules") === "true";
			return Response.json({
				org: "org-acme",
				viewer: "viewer-1",
				solutions: entitled.map((entry) => ({
					targetId: `target-${entry.id}`,
					healthy: entry.healthy ?? true,
					scopeNodeId: `node-${entry.id}`,
					...(entry.installationId === undefined ? {} : { installationId: entry.installationId }),
				})),
				...(asked && composed.length > 0
					? { composedModules: composed.map((entry) => ({ targetId: `target-${entry.id}`, installationId: entry.installationId })) }
					: {}),
			});
		}
		if (path === "/solutions/_registry") {
			return Response.json({
				revision: 1,
				solutions: registered.map((entry) => {
					const { page = true, ...record } = typeof entry === "string" ? { id: entry } : entry;
					return { ...record, status: "active", targetId: `target-${record.id}`, ...(page ? { manifest: manifest(record.id) } : {}) };
				}),
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
		expect(await res.json()).toEqual({ installationId: "inst-a", healthy: true, organizationId: "org-acme", viewerKey: "viewer-1", reach: {} });
	});

	it("hands a module page each module it declares it consumes, by the viewer's installation of it", async () => {
		vi.stubGlobal("fetch", gatewayServing([
			{ id: "people", kind: "module", reaches: ["records", "documents", "assistant"] },
			{ id: "records", kind: "module" },
			{ id: "documents", kind: "module" },
			{ id: "assistant", kind: "module" },
			{ id: "unrelated", kind: "module" },
		], [
			{ id: "people", installationId: "inst-people" },
			{ id: "records", installationId: "inst-records" },
			{ id: "documents", installationId: "inst-documents" },
			{ id: "unrelated", installationId: "inst-unrelated" },
		]));
		expect(await (await call("people")).json()).toEqual({
			installationId: "inst-people",
			healthy: true,
			organizationId: "org-acme",
			viewerKey: "viewer-1",
			// The assistant is declared but this viewer cannot open it, and the
			// unrelated module is open but undeclared: neither is reach.
			reach: { records: { installationId: "inst-records" }, documents: { installationId: "inst-documents" } },
		});
	});

	it("hands a module page each composed module without a page it consumes, by the organization's installation of it", async () => {
		const gateway = gatewayServing([
			{ id: "people", kind: "module", reaches: ["records", "documents", "assistant", "roled"] },
			{ id: "records", kind: "module", page: false },
			{ id: "documents", kind: "module", page: false },
			{ id: "assistant", kind: "module", page: false },
			{ id: "roled", kind: "module" },
		], [
			{ id: "people", installationId: "inst-people" },
		], [
			{ id: "records", installationId: "inst-records" },
			{ id: "documents", installationId: "inst-documents" },
			{ id: "roled", installationId: "inst-roled" },
		]);
		vi.stubGlobal("fetch", gateway);
		const body = await (await call("people")).json();
		// The assistant is composed in no installation of this organization, and
		// a module with a page joins only through the viewer's own entitlement,
		// whatever else names it.
		expect(body.reach).toEqual({ records: { installationId: "inst-records" }, documents: { installationId: "inst-documents" } });
		const asked = gateway.mock.calls.map(([input]) => new URL(String(input))).find((url) => url.pathname === "/solutions/_entitlements");
		expect(asked?.searchParams.get("composedModules")).toBe("true");
	});

	it("hands a solution the same binding, reaching only what it declares", async () => {
		vi.stubGlobal("fetch", gatewayServing([
			{ id: "solution-a", kind: "solution", reaches: ["records"] },
			{ id: "records", kind: "module" },
		], [
			{ id: "solution-a", installationId: "inst-a" },
			{ id: "records", installationId: "inst-records" },
		]));
		const body = await (await call("solution-a")).json();
		expect(body).toMatchObject({ installationId: "inst-a", organizationId: "org-acme", viewerKey: "viewer-1" });
		expect(body.reach).toEqual({ records: { installationId: "inst-records" } });
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
