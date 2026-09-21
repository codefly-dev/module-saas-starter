import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// The registry reaches the gateway through service discovery and reads the
// cluster-internal secret through the Codefly SDK; vi.mock is hoisted above
// module init, so the stubs must be created with vi.hoisted.
const { getWorkspaceSecret, getEndpoints } = vi.hoisted(() => ({
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(() => []),
}));
vi.mock("codefly", () => ({ getWorkspaceSecret, getEndpoints }));

import { GET } from "@/app/api/solutions/surfaces/route";

const GATEWAY = "http://gateway.internal:8080";
const TOKEN = "internal-test-token";

function manifest(
	id: string,
	surfaces?: Array<Record<string, unknown>>,
): string {
	return JSON.stringify({
		id,
		nav: { title: id.toUpperCase(), path: `/s/${id}` },
		frontend: {
			type: "module-federation",
			manifestUrl: `https://${id}.internal/mf-manifest.json`,
			exposedModule: "./Page",
		},
		surfaces,
	});
}

/** A gateway serving one registry snapshot, the only call this route makes. */
function gatewayServing(
	solutions: Array<{ id: string; status: string; manifest: string }>,
) {
	return vi.fn(async (input: string | URL) => {
		if (new URL(String(input)).pathname !== "/solutions/_registry") {
			return new Response(JSON.stringify({ error: "unexpected" }), {
				status: 500,
			});
		}
		return new Response(
			JSON.stringify({ revision: 3, leaseSeconds: 120, solutions }),
			{ status: 200, headers: { "content-type": "application/json" } },
		);
	});
}

// registry.ts caches its snapshot on globalThis so every Next module graph in a
// process shares one; drop it between tests or a stale snapshot leaks across.
function resetRegistryCache() {
	const g = globalThis as Record<string, unknown>;
	g.__solutionSnapshot = null;
	g.__solutionSnapshotInFlight = null;
}

function request(query: string): Request {
	return new Request(`http://frontend/api/solutions/surfaces${query}`);
}

const WORD_SURFACE = {
	id: "footnote",
	client: "word",
	title: "Footnote",
	description: "Cite a claim.",
	module: "/surfaces/word/footnote.js",
	contract: 1,
	applies: "always",
	events: ["documents.entry.*"],
};
const SLACK_SURFACE = {
	id: "ask",
	client: "slack",
	title: "Ask",
	module: "/surfaces/slack/ask.js",
	contract: 1,
};

describe("solutions surfaces route", () => {
	beforeEach(() => {
		resetRegistryCache();
		getWorkspaceSecret.mockReturnValue(TOKEN);
		getEndpoints.mockReturnValue([
			{ service: "auth-gateway", name: "rest", address: `${GATEWAY}/rest` },
		]);
	});

	afterEach(() => {
		getWorkspaceSecret.mockReset();
		vi.unstubAllGlobals();
		resetRegistryCache();
	});

	it("answers one client kind with the surfaces declared for it", async () => {
		vi.stubGlobal(
			"fetch",
			gatewayServing([
				{
					id: "audit",
					status: "active",
					manifest: manifest("audit", [WORD_SURFACE, SLACK_SURFACE]),
				},
			]),
		);
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(200);
		expect(await res.json()).toEqual({
			solutions: [{ id: "audit", title: "AUDIT", surfaces: [WORD_SURFACE] }],
		});
	});

	it("leaves out a solution that declares nothing for the kind", async () => {
		vi.stubGlobal(
			"fetch",
			gatewayServing([
				{
					id: "audit",
					status: "active",
					manifest: manifest("audit", [WORD_SURFACE]),
				},
				{ id: "billing", status: "active", manifest: manifest("billing") },
				{
					id: "briefing",
					status: "active",
					manifest: manifest("briefing", [SLACK_SURFACE]),
				},
			]),
		);
		const body = (await (await GET(request("?client=word"))).json()) as {
			solutions: Array<{ id: string }>;
		};
		expect(body.solutions.map((entry) => entry.id)).toEqual(["audit"]);
	});

	it("refuses a request that names no client kind", async () => {
		// Answering the unfiltered set would hand every client every other
		// client's surfaces — the opposite of asking for a kind.
		vi.stubGlobal(
			"fetch",
			gatewayServing([
				{
					id: "audit",
					status: "active",
					manifest: manifest("audit", [WORD_SURFACE, SLACK_SURFACE]),
				},
			]),
		);
		for (const query of ["", "?client="]) {
			const res = await GET(request(query));
			expect(res.status, `query ${JSON.stringify(query)}`).toBe(400);
			expect(await res.json()).toEqual({ error: "missing_client" });
		}
	});

	it("carries no deployment topology", async () => {
		// The nav projection beside this one is ungated for the same reason and
		// under the same rule: a public projection ships what a caller renders,
		// never where a solution's code or backend lives.
		vi.stubGlobal(
			"fetch",
			gatewayServing([
				{
					id: "audit",
					status: "active",
					manifest: manifest("audit", [WORD_SURFACE]),
				},
			]),
		);
		const body = await (await GET(request("?client=word"))).text();
		expect(body).not.toContain("mf-manifest.json");
		expect(body).not.toContain("serviceAlias");
		const parsed = JSON.parse(body) as {
			solutions: Array<Record<string, unknown>>;
		};
		expect(Object.keys(parsed.solutions[0] ?? {}).sort()).toEqual([
			"id",
			"surfaces",
			"title",
		]);
	});

	it("serves nothing for a solution that is not active", async () => {
		vi.stubGlobal(
			"fetch",
			gatewayServing([
				{
					id: "audit",
					status: "pending",
					manifest: manifest("audit", [WORD_SURFACE]),
				},
			]),
		);
		expect(await (await GET(request("?client=word"))).json()).toEqual({
			solutions: [],
		});
	});

	it("answers 503 rather than an empty list when the registry is unreadable", async () => {
		// An empty answer would silently retract every surface a client is
		// showing, which is indistinguishable from the solutions withdrawing them.
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("unreachable");
			}),
		);
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(503);
		expect(await res.json()).toEqual({ error: "registry_unavailable" });
	});
});
