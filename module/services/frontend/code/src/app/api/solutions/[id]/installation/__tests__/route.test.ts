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
type Registered = string | { id: string; kind: "module" | "solution"; reaches?: string[]; boundReaches?: string[]; page?: boolean };

/**
 * `composed` is the organization's composed modules, each saying whether it
 * declares roles, which the gateway answers only to a read that asks for them.
 */
function gatewayServing(
	registered: Registered[],
	entitled: Array<{ id: string; installationId?: string; healthy?: boolean }>,
	composed: Array<{ id: string; installationId: string; roleGated?: boolean | null }> = [],
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
					? {
							composedModules: composed.map((entry) => ({
								targetId: `target-${entry.id}`,
								installationId: entry.installationId,
								// null: a gateway older than the field, which omits it.
								...(entry.roleGated === null ? {} : { roleGated: entry.roleGated ?? false }),
							})),
						}
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

	it("hands a module page each composed module without roles it consumes, by the organization's installation of it", async () => {
		const gateway = gatewayServing([
			{ id: "people", kind: "module", reaches: ["records", "documents", "assistant", "roled", "bundled"] },
			{ id: "records", kind: "module", page: false },
			{ id: "documents", kind: "module" },
			{ id: "assistant", kind: "module", page: false },
			{ id: "roled", kind: "module" },
			{ id: "bundled", kind: "module", page: false },
		], [
			{ id: "people", installationId: "inst-people" },
		], [
			{ id: "records", installationId: "inst-records" },
			{ id: "documents", installationId: "inst-documents" },
			{ id: "roled", installationId: "inst-roled", roleGated: true },
			{ id: "bundled", installationId: "inst-bundled", roleGated: true },
		]);
		vi.stubGlobal("fetch", gateway);
		const body = await (await call("people")).json();
		// The assistant is composed in no installation of this organization.
		// A module declaring roles that the page calls on the person's own
		// session, remote page or host-bundled, reaches the viewer by grant
		// alone, and this viewer holds none.
		expect(body.reach).toEqual({ records: { installationId: "inst-records" }, documents: { installationId: "inst-documents" } });
		const asked = gateway.mock.calls.map(([input]) => new URL(String(input))).find((url) => url.pathname === "/solutions/_entitlements");
		expect(asked?.searchParams.get("composedModules")).toBe("true");
	});

	it("hands a page each module it consumes through its own binding, by the organization's installation, though the module declares roles the viewer holds none of", async () => {
		// The directory case: a host-bundled role-gated page (no remote, so
		// no manifest) and a remote role-gated page, both consumed through the
		// page's bindings, whose calls carry the viewer's role at the PAGE.
		vi.stubGlobal("fetch", gatewayServing([
			{ id: "people", kind: "module", reaches: ["records", "documents", "assistant"], boundReaches: ["records", "documents"] },
			{ id: "records", kind: "module", page: false },
			{ id: "documents", kind: "module" },
			{ id: "assistant", kind: "module", page: false },
		], [
			{ id: "people", installationId: "inst-people" },
		], [
			{ id: "records", installationId: "inst-records", roleGated: true },
			{ id: "documents", installationId: "inst-documents", roleGated: true },
			{ id: "assistant", installationId: "inst-assistant", roleGated: true },
		]));
		const body = await (await call("people")).json();
		// The assistant is an edge dependency only, called on the person's own
		// session: a role-gated module there is still reached by grant alone.
		expect(body.reach).toEqual({ records: { installationId: "inst-records" }, documents: { installationId: "inst-documents" } });
	});

	it("hands a page a host-bundled module page the viewer holds a role on, by the viewer's installation of it", async () => {
		vi.stubGlobal("fetch", gatewayServing([
			{ id: "people", kind: "module", reaches: ["records"] },
			{ id: "records", kind: "module", page: false },
		], [
			{ id: "people", installationId: "inst-people" },
			{ id: "records", installationId: "inst-records" },
		]));
		// No composition answer is needed: the registry carries no remote for
		// the page, so its target is the join, and the viewer's own
		// entitlement names it.
		expect((await (await call("people")).json()).reach).toEqual({ records: { installationId: "inst-records" } });
	});

	it("reads a composed module an older gateway does not flag as role-gated, so only a bound reach names it", async () => {
		vi.stubGlobal("fetch", gatewayServing([
			{ id: "people", kind: "module", reaches: ["records", "assistant", "absent"], boundReaches: ["records", "absent"] },
			{ id: "records", kind: "module", page: false },
			{ id: "assistant", kind: "module", page: false },
			{ id: "absent", kind: "module", page: false },
		], [
			{ id: "people", installationId: "inst-people" },
		], [
			{ id: "records", installationId: "inst-records", roleGated: null },
			{ id: "assistant", installationId: "inst-assistant", roleGated: null },
		]));
		// The assistant is an edge reach and unflagged, so it fails closed; a
		// bound reach with no composition installation stays absent.
		expect((await (await call("people")).json()).reach).toEqual({ records: { installationId: "inst-records" } });
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
