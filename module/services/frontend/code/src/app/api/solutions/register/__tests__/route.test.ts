import { generateKeyPairSync, sign } from "node:crypto";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// The route reads the cluster-internal secret via the Codefly SDK, and the
// registry reaches the gateway through service discovery. vi.mock is hoisted
// above module init, so the stubs must be created with vi.hoisted.
const { getWorkspaceSecret, getEndpoints } = vi.hoisted(() => ({
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(() => []),
}));
vi.mock("codefly", () => ({ getWorkspaceSecret, getEndpoints }));

import { DELETE, GET, POST } from "@/app/api/solutions/register/route";
import {
	findSolution,
	navProjection,
	type SolutionManifest,
} from "@/solutions/registry";

const TOKEN = "internal-test-token";
const { publicKey, privateKey } = generateKeyPairSync("ed25519");
const KEY_ID = "test-key";
const JWKS = JSON.stringify({
	keys: [
		{
			...publicKey.export({ format: "jwk" }),
			alg: "EdDSA",
			use: "sig",
			kid: KEY_ID,
		},
	],
});

let jtiCounter = 0;

function base64url(value: object): string {
	return Buffer.from(JSON.stringify(value), "utf8").toString("base64url");
}

/** A credential accounts would mint: EdDSA, bound to one id and one publisher. */
function credential(solution = "audit", subject = `solution:${solution}`): string {
	const now = Math.floor(Date.now() / 1000);
	const head = base64url({ alg: "EdDSA", typ: "JWT", kid: KEY_ID });
	const payload = base64url({
		iss: "saas-starter",
		sub: subject,
		aud: ["solution-registration"],
		solution,
		iat: now,
		exp: now + 300,
		jti: `jti-${jtiCounter++}`,
	});
	const signature = sign(
		null,
		Buffer.from(`${head}.${payload}`, "utf8"),
		privateKey,
	).toString("base64url");
	return `${head}.${payload}.${signature}`;
}
const GATEWAY = "http://gateway.internal:8080";

// A stand-in for the durable registry behind the gateway: enough of the wire
// contract for the route to be exercised end to end, with the revision counter
// that makes a write observable.
function fakeGateway() {
	const stored = new Map<string, string>();
	let revision = 0;
	const respond = (body: unknown, status = 200) =>
		new Response(JSON.stringify(body), {
			status,
			headers: { "content-type": "application/json" },
		});
	return vi.fn(async (input: string | URL, init?: RequestInit) => {
		const url = new URL(String(input));
		if (url.pathname === "/v1/auth/.well-known/jwks.json") {
			return new Response(JWKS, { status: 200 });
		}
		if (url.pathname === "/solutions/_frontend") {
			const body = JSON.parse(String(init?.body ?? "{}"));
			stored.set(body.id, body.manifest);
			revision += 1;
			return respond({ ok: true, id: body.id, revision, status: "active" });
		}
		if (url.pathname === "/solutions/_register" && init?.method === "DELETE") {
			stored.delete(url.searchParams.get("id") ?? "");
			revision += 1;
			return respond({ ok: true, revision });
		}
		if (url.pathname === "/solutions/_registry") {
			return respond({
				revision,
				leaseSeconds: 120,
				solutions: [...stored].map(([id, manifest]) => ({
					id,
					status: "active",
					manifest,
				})),
			});
		}
		return respond({ error: "unexpected" }, 500);
	});
}

/**
 * A fetch stub that answers the JWKS lookup normally and drives every registry
 * call to one outcome. The credential check and the registry write share a
 * fetch, so a stub that only models the registry refuses the credential first
 * and the test would pass for the wrong reason.
 */
function registryAnswering(response?: Response, onRegistry?: () => never) {
	return vi.fn(async (input: string | URL) => {
		if (new URL(String(input)).pathname === "/v1/auth/.well-known/jwks.json") {
			return new Response(JWKS, { status: 200 });
		}
		if (onRegistry) onRegistry();
		return response!.clone();
	});
}

// registry.ts caches its snapshot on globalThis so every Next module graph in a
// process shares one; drop it between tests or a stale snapshot leaks across.
function resetRegistryCache() {
	const g = globalThis as Record<string, unknown>;
	g.__solutionSnapshot = null;
	g.__solutionSnapshotInFlight = null;
}

function manifestBody(id = "audit") {
	return {
		id,
		nav: { title: "Audit", path: `/s/${id}` },
		frontend: {
			type: "module-federation",
			manifestUrl: "https://audit.internal/mf-manifest.json",
			exposedModule: "./Page",
		},
	};
}

function postRequest(body: unknown, token?: string): Request {
	const headers: Record<string, string> = {
		"content-type": "application/json",
	};
	if (token !== undefined) {
		headers["x-codefly-internal-token"] = token;
		const id =
			typeof body === "object" && body !== null
				? String((body as { id?: unknown }).id ?? "audit")
				: "audit";
		headers["x-codefly-solution-registration"] = credential(id);
	}
	return new Request("http://frontend/api/solutions/register", {
		method: "POST",
		headers,
		body: JSON.stringify(body),
	});
}

describe("solutions register route auth", () => {
	beforeEach(() => {
		resetRegistryCache();
		const g = globalThis as Record<string, unknown>;
		g.__solutionRegistrationJwks = undefined;
		g.__solutionRegistrationJtis = undefined;
		getEndpoints.mockReturnValue([
			{ service: "auth-gateway", name: "rest", address: `${GATEWAY}/rest` },
		]);
		vi.stubGlobal("fetch", fakeGateway());
	});

	afterEach(async () => {
		getWorkspaceSecret.mockReset();
		// Clean any registration this suite added.
		getWorkspaceSecret.mockReturnValue(TOKEN);
		await DELETE(
			new Request("http://frontend/api/solutions/register?id=audit", {
				method: "DELETE",
				headers: { "x-codefly-internal-token": TOKEN },
			}),
		);
		getWorkspaceSecret.mockReset();
		vi.unstubAllGlobals();
		resetRegistryCache();
	});

	it("rejects a POST with no internal token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await POST(postRequest(manifestBody()));
		expect(res.status).toBe(401);
	});

	it("refuses a POST carrying the cluster-internal token but no credential", async () => {
		// The shared token attests to no particular publisher, which is why it is
		// no longer sufficient here: it is exactly what let any holder claim any id.
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await POST(
			new Request("http://frontend/api/solutions/register", {
				method: "POST",
				headers: {
					"content-type": "application/json",
					"x-codefly-internal-token": TOKEN,
				},
				body: JSON.stringify(manifestBody()),
			}),
		);
		expect(res.status).toBe(401);
	});

	it("refuses a POST whose credential names another solution", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await POST(
			new Request("http://frontend/api/solutions/register", {
				method: "POST",
				headers: {
					"content-type": "application/json",
					"x-codefly-solution-registration": credential("example-other"),
				},
				body: JSON.stringify(manifestBody()),
			}),
		);
		expect(res.status).toBe(403);
	});

	it("accepts a POST with the correct internal token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await POST(postRequest(manifestBody(), TOKEN));
		expect(res.status).toBe(200);
		// The revision the write landed at comes back, so a registrant can hold
		// it and drive its own compare-and-swap next time.
		await expect(res.json()).resolves.toMatchObject({
			ok: true,
			id: "audit",
			status: "active",
			revision: 1,
		});
	});

	it("relays a registry conflict rather than reporting success", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		vi.stubGlobal("fetch", registryAnswering(new Response("conflict", { status: 409 })));
		expect((await POST(postRequest(manifestBody(), TOKEN))).status).toBe(409);
	});

	it("relays a foreign-publisher refusal", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		vi.stubGlobal("fetch", registryAnswering(new Response("forbidden", { status: 403 })));
		expect((await POST(postRequest(manifestBody(), TOKEN))).status).toBe(403);
	});

	it("reports a registry outage instead of a phantom success", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		vi.stubGlobal(
			"fetch",
			registryAnswering(undefined, () => {
				throw new Error("gateway unreachable");
			}),
		);
		expect((await POST(postRequest(manifestBody(), TOKEN))).status).toBe(503);
	});

	it("answers 503, not an empty list, when the registry cannot be read", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("gateway unreachable");
			}),
		);
		resetRegistryCache();
		expect((await GET()).status).toBe(503);
	});

	it("rejects an authenticated POST carrying an unsafe manifest", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const bad = manifestBody("evil");
		(bad.frontend as Record<string, unknown>).manifestUrl =
			"javascript:alert(1)";
		const res = await POST(postRequest(bad, TOKEN));
		expect(res.status).toBe(422);
	});

	it("lets the browser GET the nav list without a token", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const res = await GET();
		expect(res.status).toBe(200);
		await expect(res.json()).resolves.toHaveProperty("solutions");
	});

	it("projects the public nav list down to id and nav", async () => {
		getWorkspaceSecret.mockReturnValue(TOKEN);
		const body = manifestBody() as Record<string, unknown>;
		body.dashboard = {
			events: [{ name: "login", type: "auth.login.v1" }],
			metrics: [
				{
					id: "logins",
					kind: "source",
					filter: { event: "login" },
					groupBy: "time",
					bucket: "day",
					aggregation: "count",
				},
			],
			dashboards: [
				{
					id: "activity",
					layout: "grid",
					widgets: [
						{ id: "logins", metric: "logins", visualization: "line" },
					],
				},
			],
		};
		expect((await POST(postRequest(body, TOKEN))).status).toBe(200);

		const listed = (await GET().then((r) => r.json())) as {
			solutions: Array<Record<string, unknown>>;
		};
		const audit = listed.solutions.find((s) => s.id === "audit");
		expect(audit).toBeDefined();
		expect(audit?.nav).toMatchObject({ title: "Audit", path: "/s/audit" });
		// Everything the manifest carries beyond the nav entry is deployment
		// topology: where the solution's code is served from, which backend
		// fronts it, and its dashboard declaration. This response is readable by
		// every signed-in browser, so it must carry none of it.
		expect(Object.keys(audit ?? {}).sort()).toEqual(["id", "nav"]);
	});

	it("keeps a mutated nav projection out of the stored manifest", async () => {
		// The projection copies the nav object rather than aliasing it, so a
		// caller that mutates a projected value cannot reach the registry.
		//
		// This asserts against navProjection's own return value, NOT against a
		// parsed GET body: `await response.json()` is a fresh object, so mutating
		// it could never reach the registry however navProjection was written,
		// and a test framed that way passes with the aliasing bug in place.
		getWorkspaceSecret.mockReturnValue(TOKEN);
		expect((await POST(postRequest(manifestBody(), TOKEN))).status).toBe(200);

		const stored = await findSolution("audit");
		expect(stored).not.toBeNull();
		expect(stored).not.toBe("unavailable");
		const projected = navProjection(stored as SolutionManifest);
		projected.nav.title = "Tampered";

		expect((await findSolution("audit")) as SolutionManifest).toMatchObject({
			nav: { title: "Audit" },
		});
		const listed = (await GET().then((r) => r.json())) as {
			solutions: Array<{ id: string; nav: { title: string } }>;
		};
		expect(listed.solutions.find((s) => s.id === "audit")?.nav.title).toBe(
			"Audit",
		);
	});
});
