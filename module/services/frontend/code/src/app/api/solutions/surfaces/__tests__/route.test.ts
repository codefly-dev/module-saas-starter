import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// The registry reaches the gateway through service discovery and reads the
// cluster-internal secret through the Codefly SDK; vi.mock is hoisted above
// module init, so the stubs must be created with vi.hoisted.
const { getWorkspaceSecret, getEndpoints, getWorkspaceConfiguration } =
	vi.hoisted(() => ({
		getWorkspaceSecret:
			vi.fn<(name: string, key: string) => string | undefined>(),
		getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(() => []),
		getWorkspaceConfiguration:
			vi.fn<(name: string, key: string) => string | undefined>(),
	}));
vi.mock("codefly", () => ({
	getWorkspaceSecret,
	getEndpoints,
	getWorkspaceConfiguration,
	getCurrentModule: () => "",
	getCurrentService: () => "",
}));

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

interface EntitlementAnswer {
	/** Solution ids the viewer may use; defaults to every registered one. */
	usable?: string[];
	/** Ids whose installation is unhealthy — entitled, but not routable. */
	unhealthy?: string[];
	/** Answer the entitlement read with this status instead of 200. */
	status?: number;
	/** The gateway's own refusal name (X-Codefly-Entitlement-Refusal), if any. */
	refusal?: string;
	org?: string;
	viewer?: string;
}

/**
 * A gateway serving the two reads this route makes: the registry snapshot, and
 * the per-viewer entitlement projection that narrows it (#949).
 */
function gatewayServing(
	solutions: Array<{ id: string; status: string; manifest?: string }>,
	entitlements: EntitlementAnswer = {},
) {
	return vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
		const target =
			input instanceof Request ? input.url : String(input as string | URL);
		const path = new URL(target).pathname;
		if (path === "/solutions/_entitlements") {
			if (entitlements.status !== undefined && entitlements.status !== 200) {
				const headers = new Headers();
				if (entitlements.refusal) {
					headers.set("X-Codefly-Entitlement-Refusal", entitlements.refusal);
				}
				return new Response(JSON.stringify({ error: "refused" }), {
					status: entitlements.status,
					headers,
				});
			}
			// The gateway answers 401 itself when no credential was forwarded; this
			// stub stands in for ext_authz making that call.
			const headers = new Headers(init?.headers);
			if (!headers.get("authorization")) {
				return new Response(JSON.stringify({ error: "unauthenticated" }), {
					status: 401,
				});
			}
			const usable = entitlements.usable ?? solutions.map((s) => s.id);
			return new Response(
				JSON.stringify({
					org: entitlements.org ?? "org-acme",
					viewer: entitlements.viewer ?? "viewer-1",
					solutions: usable.map((id) => ({
						targetId: `target-${id}`,
						healthy: !(entitlements.unhealthy ?? []).includes(id),
						scopeNodeId: `node-${id}`,
					})),
				}),
				{ status: 200, headers: { "content-type": "application/json" } },
			);
		}
		if (path !== "/solutions/_registry") {
			return new Response(JSON.stringify({ error: "unexpected" }), {
				status: 500,
			});
		}
		return new Response(
			JSON.stringify({
				revision: 3,
				// The host stamps each record's target from its declaration, and
				// the entitlement answer above derives the same value from the
				// alias — which is what lets the projection join them. A fixture
				// that left this out would produce manifests with no target, and
				// every projection would be legitimately empty.
				solutions: solutions.map((entry) => ({
					targetId: `target-${entry.id}`,
					...entry,
				})),
			}),
			{ status: 200, headers: { "content-type": "application/json" } },
		);
	});
}

// registry.ts caches its snapshot on globalThis so every Next module graph in a
// process shares one; drop it between tests or a stale snapshot leaks across.
// The projection cache is anchored there for the same reason and is keyed on the
// authority revision, so it must be dropped too — otherwise one test's menu
// answers another's viewer.
function resetRegistryCache() {
	const g = globalThis as Record<string, unknown>;
	g.__solutionSnapshot = null;
	g.__solutionSnapshotInFlight = null;
	g.__solutionProjectionCache = new Map();
}

function request(query: string, credential = "Bearer viewer-token"): Request {
	const headers = new Headers();
	if (credential) headers.set("authorization", credential);
	return new Request(`http://frontend/api/solutions/surfaces${query}`, {
		headers,
	});
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
			solutions: [
				{
					id: "audit",
					title: "AUDIT",
					origin: "https://audit.internal",
					available: true,
					surfaces: [WORD_SURFACE],
				},
			],
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

	it("refuses a client kind the registry could never have stored", async () => {
		// Answering [] would read as "nothing is offered for you" and send the
		// caller auditing its registration instead of its spelling.
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
		for (const kind of ["Word", "%20", "wo rd", "-word", "word."]) {
			const res = await GET(request(`?client=${kind}`));
			expect(res.status, `kind ${JSON.stringify(kind)}`).toBe(400);
			expect(await res.json()).toEqual({ error: "invalid_client" });
		}
	});

	it("carries no deployment topology", async () => {
		// The nav projection beside this one narrows the same way and obeys the
		// same rule: a viewer-facing projection ships what a caller renders, never
		// where a solution's code or backend lives.
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
		// The origin is carried; the manifest path and backend service are not.
		expect(body).toContain("https://audit.internal");
		expect(body).not.toContain("mf-manifest.json");
		expect(body).not.toContain("serviceAlias");
		const parsed = JSON.parse(body) as {
			solutions: Array<Record<string, unknown>>;
		};
		expect(Object.keys(parsed.solutions[0] ?? {}).sort()).toEqual([
			"available",
			"id",
			"origin",
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
		// The entitlement read succeeds here so the 503 is attributable to the
		// registry and not to the authority beside it.
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
				const target =
					input instanceof Request ? input.url : String(input as string | URL);
				if (new URL(target).pathname === "/solutions/_entitlements") {
					const headers = new Headers(init?.headers);
					expect(headers.get("authorization")).toBe("Bearer viewer-token");
					return new Response(
						JSON.stringify({
							org: "org-acme",
							viewer: "viewer-1",
							solutions: [
								{
									targetId: "target-audit",
									healthy: true,
									scopeNodeId: "node-audit",
								},
							],
						}),
						{ status: 200, headers: { "content-type": "application/json" } },
					);
				}
				throw new Error("unreachable");
			}),
		);
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(503);
		expect(await res.json()).toEqual({ error: "registry_unavailable" });
	});

	// -----------------------------------------------------------------------
	// Per-organization, per-viewer narrowing (issue #949)
	// -----------------------------------------------------------------------

	const audit = {
		id: "audit",
		status: "active",
		manifest: manifest("audit", [WORD_SURFACE]),
	};
	const ledger = {
		id: "ledger",
		status: "active",
		manifest: manifest("ledger", [WORD_SURFACE]),
	};

	it("answers 401, not an empty list, to an unauthenticated caller", async () => {
		// [] would make "you are not signed in" and "your organization installed
		// nothing" the same answer, and a client told the second audits its
		// registration instead of its session.
		vi.stubGlobal("fetch", gatewayServing([audit]));
		const res = await GET(request("?client=word", ""));
		expect(res.status).toBe(401);
		expect(await res.json()).toEqual({ error: "unauthenticated" });
	});

	it("refuses before reading the registry, so it is no availability oracle", async () => {
		// An unauthenticated caller must not be able to learn whether the registry
		// is healthy by comparing 401 against 503.
		const fetchMock = gatewayServing([audit]);
		vi.stubGlobal("fetch", fetchMock);
		await GET(request("?client=word", ""));
		const paths = fetchMock.mock.calls.map(
			(call) =>
				new URL(
					call[0] instanceof Request
						? call[0].url
						: String(call[0] as string | URL),
				).pathname,
		);
		expect(paths).toEqual(["/solutions/_entitlements"]);
	});

	it("answers 403 when the session carries no organization", async () => {
		vi.stubGlobal(
			"fetch",
			gatewayServing([audit], { status: 403, refusal: "no-organization" }),
		);
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(403);
		expect(await res.json()).toEqual({ error: "no_organization" });
	});

	it("answers 403 forbidden, not no_organization, for an ext_authz refusal", async () => {
		// Only the gateway's own named refusal means "no organization"; a bare 403
		// is a verdict on the user's credential and must not be relabelled.
		vi.stubGlobal("fetch", gatewayServing([audit], { status: 403 }));
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(403);
		expect(await res.json()).toEqual({ error: "forbidden" });
	});

	it("answers 503, not 401, when the gateway refuses this frontend's internal credential", async () => {
		// A deployment fault on the frontend's own credential. Relayed as 401 it
		// would tell every signed-in viewer to re-authenticate, and make the menu's
		// authedFetch rotate the refresh token on every poll.
		vi.stubGlobal(
			"fetch",
			gatewayServing([audit], { status: 401, refusal: "internal-credential" }),
		);
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(503);
		expect(await res.json()).toEqual({ error: "authority_unavailable" });
	});

	it("relays 429 when the organization spent its read budget", async () => {
		vi.stubGlobal("fetch", gatewayServing([audit], { status: 429 }));
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(429);
		expect(await res.json()).toEqual({ error: "rate_limited" });
	});

	it("answers 503, never an empty list, when the authority cannot answer", async () => {
		vi.stubGlobal("fetch", gatewayServing([audit], { status: 500 }));
		const res = await GET(request("?client=word"));
		expect(res.status).toBe(503);
		expect(await res.json()).toEqual({ error: "authority_unavailable" });
	});

	it("hides a deployed solution the organization has not installed", async () => {
		vi.stubGlobal(
			"fetch",
			gatewayServing([audit, ledger], { usable: ["audit"] }),
		);
		const body = (await (await GET(request("?client=word"))).json()) as {
			solutions: Array<{ id: string }>;
		};
		expect(body.solutions.map((entry) => entry.id)).toEqual(["audit"]);
	});

	it("gives two viewers in one organization different surface lists", async () => {
		// The acceptance case: same deployment, same registered set, different
		// grants — so the answer is a function of the viewer, not the deployment.
		vi.stubGlobal(
			"fetch",
			gatewayServing([audit, ledger], {
				viewer: "viewer-in-reviewers",
				usable: ["audit"],
			}),
		);
		const first = (await (await GET(request("?client=word"))).json()) as {
			solutions: Array<{ id: string }>;
		};
		expect(first.solutions.map((entry) => entry.id)).toEqual(["audit"]);

		resetRegistryCache();
		vi.stubGlobal(
			"fetch",
			gatewayServing([audit, ledger], {
				viewer: "viewer-in-clerks",
				usable: ["ledger"],
			}),
		);
		const second = (await (await GET(request("?client=word"))).json()) as {
			solutions: Array<{ id: string }>;
		};
		expect(second.solutions.map((entry) => entry.id)).toEqual(["ledger"]);
	});

	it("narrows after a revocation without the registry changing", async () => {
		vi.stubGlobal(
			"fetch",
			gatewayServing([audit, ledger], { usable: ["audit", "ledger"] }),
		);
		const before = (await (await GET(request("?client=word"))).json()) as {
			solutions: Array<{ id: string }>;
		};
		expect(before.solutions.map((entry) => entry.id)).toEqual([
			"audit",
			"ledger",
		]);

		// The grant on `ledger` is revoked. Only the authority answer changes —
		// and because the projection cache is keyed on a digest of it, the cached
		// menu cannot survive the grant that justified it.
		vi.stubGlobal(
			"fetch",
			gatewayServing([audit, ledger], { usable: ["audit"] }),
		);
		const after = (await (await GET(request("?client=word"))).json()) as {
			solutions: Array<{ id: string }>;
		};
		expect(after.solutions.map((entry) => entry.id)).toEqual(["audit"]);
	});

	it("keeps an unhealthy installation listed and marks it unavailable", async () => {
		vi.stubGlobal("fetch", gatewayServing([audit], { unhealthy: ["audit"] }));
		const body = (await (await GET(request("?client=word"))).json()) as {
			solutions: Array<{ id: string; available: boolean }>;
		};
		// Listed, because the org installed it and the viewer was granted it.
		// Unavailable, because it must not be routed as though it were serving.
		expect(body.solutions).toHaveLength(1);
		expect(body.solutions[0]?.available).toBe(false);
	});
});
