import { AccessibleScopeService } from "@codefly-dev/saas-sdk";
import { createClient, Code } from "@connectrpc/connect";
import { moduleTransport, requestBinding } from "@codefly-dev/saas-ui/solution";
import { HOST_SETUP_PROCEDURES } from "@/lib/host-setup-procedures";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// The route resolves the gateway base (getEndpoints) and the first-party trust
// context (getCurrentModule/getCurrentService/getWorkspaceSecret/
// getWorkspaceConfiguration, via resolveCodeflyGatewayContext). vi.mock is
// hoisted above module init, so the stubs are created with vi.hoisted.
const {
	getEndpoints,
	getCurrentModule,
	getCurrentService,
	getWorkspaceSecret,
	getWorkspaceConfiguration,
} = vi.hoisted(() => ({
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(),
	getCurrentModule: vi.fn<() => string>(() => ""),
	getCurrentService: vi.fn<() => string>(() => ""),
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
	getWorkspaceConfiguration:
		vi.fn<(name: string, key: string) => string | undefined>(),
}));
vi.mock("codefly", async () => ({
	...(await vi.importActual<typeof import("codefly")>("codefly")),
	networkInstance: () => {
		const endpoints = getEndpoints();
		if (endpoints.length !== 1) throw new Error("gateway unavailable");
		return endpoints[0];
	},
	getEndpoints,
	getCurrentModule,
	getCurrentService,
	getWorkspaceSecret,
	getWorkspaceConfiguration,
}));

// The registry is a durable, gateway-brokered record rather than a map in this
// process, so the fixtures stub the one read the route makes instead of
// performing a real registration over the mocked fetch.
const { findSolution } = vi.hoisted(() => ({
	findSolution: vi.fn<(id: string) => Promise<unknown>>(),
}));
vi.mock("@/solutions/registry", () => ({ findSolution }));

import { GET, POST } from "@/app/api/solutions/[id]/proxy/[...path]/route";

const GATEWAY = "http://gateway.internal:8080";
// The composed endpoint carries a base path, which the route must preserve:
// src/proxy.ts forwards a host page's own product API call to `${rest}${path}`,
// so a solution's call has to land on the same URL, not on the bare origin.
const GATEWAY_BASE = `${GATEWAY}/rest`;
const INTERNAL_TOKEN = "trusted-internal-token";

function withGateway() {
	getEndpoints.mockReturnValue([
		{ service: "auth-gateway", name: "rest", address: GATEWAY_BASE },
	]);
}

// Resolve the first-party trust context the way a real runtime does: the public
// origin comes from the deployment's own configuration and from nothing else, so a
// test that wants a different one configures it rather than sending a header.
function withTrustContext(publicOrigin = "http://frontend") {
	getCurrentModule.mockReturnValue("");
	getCurrentService.mockReturnValue("");
	getWorkspaceSecret.mockImplementation((name, key) =>
		name === "internal-auth" && key === "CODEFLY_INTERNAL_TOKEN"
			? INTERNAL_TOKEN
			: undefined,
	);
	getWorkspaceConfiguration.mockImplementation((name, key) =>
		name === "application" && key === "APP_BASE_URL" ? publicOrigin : undefined,
	);
}

function registerAudit(serviceAlias = "audit-backend", kind: "module" | "solution" = "solution") {
	findSolution.mockImplementation(async (id: string) =>
		id === "audit"
			? {
					id: "audit",
					kind,
					nav: { title: "Audit", path: "/s/audit" },
					frontend: {
						type: "module-federation",
						manifestUrl: "https://audit.internal/mf-manifest.json",
						exposedModule: "./Page",
					},
					backend: { serviceAlias },
				}
			: null,
	);
}

function context(id: string, path?: string[]) {
	return { params: Promise.resolve({ id, path }) };
}

function proxyRequest(
	url: string,
	init?: RequestInit & { headers?: Record<string, string> },
): Request {
	return new Request(url, init);
}

// `Cookie`, `Origin`, and `Sec-*` are forbidden headers a fetch Request strips
// (happy-dom enforces this), so model the real Next server request — which does
// carry them — with a Headers-backed object that preserves them.
function rawRequest(
	url: string,
	method: string,
	headers: Record<string, string>,
): Request {
	return {
		method,
		url,
		headers: new Headers(headers),
		arrayBuffer: async () => new ArrayBuffer(0),
	} as unknown as Request;
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
	// resolveCodeflyGatewayContext calls .trim() on the module/service names, so
	// they must always return a string; the trust token defaults to absent.
	getCurrentModule.mockReturnValue("");
	getCurrentService.mockReturnValue("");
	getWorkspaceSecret.mockReturnValue(undefined);
	// The public origin is operator configuration, and the same-origin check is made
	// against it, so every fixture pins the one these requests arrive on. A test
	// about the origin itself overrides it (withTrustContext).
	getWorkspaceConfiguration.mockImplementation((name, key) =>
		name === "application" && key === "APP_BASE_URL"
			? "http://frontend"
			: undefined,
	);
	findSolution.mockResolvedValue(null);
	fetchMock = vi.fn(
		async () =>
			new Response("upstream-body", {
				status: 200,
				headers: { "content-type": "application/json" },
			}),
	);
	vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
	findSolution.mockReset();
	getEndpoints.mockReset();
	getCurrentModule.mockReset();
	getWorkspaceConfiguration.mockReset();
	getCurrentService.mockReset();
	getWorkspaceSecret.mockReset();
	vi.unstubAllGlobals();
});

describe("solution proxy passthrough", () => {
	it("forwards the caller bearer to the resolved gateway upstream", async () => {
		withGateway();
		registerAudit();

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
		const [target, init] = fetchMock.mock.calls[0];
		expect(target).toBe(`${GATEWAY_BASE}/v1/audit-backend/records`);
		expect((init.headers as Headers).get("authorization")).toBe(
			"Bearer caller-token",
		);
	});

	it.each([
		"saas.accounts.v1.DatasourceService/ListSources",
		"saas.store.v1.StoreService/Get",
	])(
		"forwards platform procedure %s to the gateway root",
		async (procedure) => {
			withGateway();
			withTrustContext();
			registerAudit();
			const body = JSON.stringify({ pageSize: 10 });

			const res = await POST(
				proxyRequest(
					`http://frontend/api/solutions/audit/proxy/${procedure}?key=value`,
					{
						method: "POST",
						headers: {
							authorization: "Bearer caller-token",
							"content-type": "application/json",
							"x-codefly-internal-token": "untrusted-token",
						},
						body,
					},
				),
				context("audit", procedure.split("/")),
			);

			expect(res.status).toBe(200);
			expect(await res.text()).toBe("upstream-body");
			expect(fetchMock).toHaveBeenCalledTimes(1);
			const [target, init] = fetchMock.mock.calls[0];
			expect(target).toBe(`${GATEWAY_BASE}/${procedure}?key=value`);
			expect(init.method).toBe("POST");
			expect(new TextDecoder().decode(init.body as ArrayBuffer)).toBe(body);
			const headers = init.headers as Headers;
			expect(headers.get("authorization")).toBe("Bearer caller-token");
			expect(headers.get("content-type")).toBe("application/json");
			expect(headers.get("x-codefly-internal-token")).toBe(INTERNAL_TOKEN);
			expect(headers.get("x-codefly-public-origin")).toBe("http://frontend");
		},
	);

	// `gatewayBase` resolved the endpoint to `new URL(address).origin`, dropping
	// any base path the composition put on it, while `src/proxy.ts` keeps it
	// (server/accounts-bindings.mjs normalises with toString()). The identical
	// procedure therefore reached two different URLs depending on whether a host
	// page or a solution remote issued it. Pin both ends of the normalisation.
	it.each([
		["http://gateway.internal:8080/rest", "http://gateway.internal:8080/rest"],
		[
			"http://gateway.internal:8080/api/v2/",
			"http://gateway.internal:8080/api/v2",
		],
		["http://gateway.internal:8080", "http://gateway.internal:8080"],
		["http://gateway.internal:8080/", "http://gateway.internal:8080"],
	])("preserves the gateway base path in %s", async (address, base) => {
		getEndpoints.mockReturnValue([
			{ service: "auth-gateway", name: "rest", address },
		]);
		withTrustContext();
		registerAudit();
		const procedure = "saas.accounts.v1.DatasourceService/ListSources";

		await POST(
			proxyRequest(`http://frontend/api/solutions/audit/proxy/${procedure}`, {
				method: "POST",
				headers: { "content-type": "application/json" },
				body: "{}",
			}),
			context("audit", procedure.split("/")),
		);

		expect(fetchMock.mock.calls[0][0]).toBe(`${base}/${procedure}`);
	});

	// The behavioural contract behind `SolutionPageProps.apiBase`: a remote gets
	// ONE base, and the kit components the host hands it reach the HOST's
	// services over that same base. Every procedure below is one the kit's
	// datasource client issues (packages/saas-ui/src/datasources/gateway.ts). If
	// the platform branch ever stops matching one, `<DatasourcesPanel>` mounted
	// by a solution 404s against that solution's upstream — which is exactly the
	// symptom that gets misread as "the host never gave the remote a usable
	// base" and answered by inventing a second one.
	it.each([
		"saas.accounts.v1.DatasourceService/ListSources",
		"saas.accounts.v1.DatasourceService/AddGitHubSource",
		"saas.accounts.v1.DatasourceService/BeginGitHubAppSetup",
		"saas.accounts.v1.DatasourceService/CompleteGitHubAppSetup",
		"saas.accounts.v1.DatasourceService/MigrateGitHubSourceToApp",
		"saas.accounts.v1.DatasourceService/SyncSource",
		"saas.accounts.v1.DatasourceService/DeleteSource",
		"saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes",
		"saas.accounts.v1.AuditService/QueryAuditLog",
		// `<SolutionSetup binding>`: the Setup tab's host installation procedures.
		...HOST_SETUP_PROCEDURES.map((procedure) => procedure.slice(1)),
	])(
		"reaches the host over the solution's own apiBase for %s",
		async (procedure) => {
			withGateway();
			withTrustContext();
			registerAudit();

			await POST(
				proxyRequest(`http://frontend/api/solutions/audit/proxy/${procedure}`, {
					method: "POST",
					headers: { "content-type": "application/json" },
					body: "{}",
				}),
				context("audit", procedure.split("/")),
			);

			const target = fetchMock.mock.calls[0][0] as string;
			expect(target).toBe(`${GATEWAY_BASE}/${procedure}`);
			expect(target).not.toContain("audit-backend");
		},
	);

	it.each([
		"records",
		"example.audit.v1.AuditService/ListRecords",
		"saas.accounts.v1.DatasourceService",
		"saas.accounts.v1.DatasourceService/ListSources/extra",
		"saas.accounts.v1.DatasourceService/not-a-method",
		"saas.accounts.v2.DatasourceService/ListSources",
		// Only the Setup tab's host procedures reach the host; the rest of the
		// interface stays the solution's own path.
		"host.installation.v1.InstallationService/Install",
		"host.installation.v1.InstallationService/Current",
		"host.authority.v1.AuthorityService/Issue",
	])("keeps solution path %s on the registered upstream", async (path) => {
		withGateway();
		registerAudit();

		await POST(
			proxyRequest(`http://frontend/api/solutions/audit/proxy/${path}`, {
				method: "POST",
				body: "{}",
			}),
			context("audit", path.split("/")),
		);

		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(fetchMock.mock.calls[0][0]).toBe(
			`${GATEWAY_BASE}/v1/audit-backend/${path}`,
		);
	});

	it.each(["records", "saas.accounts.v1.DatasourceService/ListSources"])(
		"rejects a cross-site request before reaching any upstream (%s)",
		async (path) => {
			withGateway();
			withTrustContext();
			registerAudit();

			const res = await POST(
				rawRequest(
					`http://frontend/api/solutions/audit/proxy/${path}`,
					"POST",
					{
						cookie: "codefly_session=1",
						"sec-fetch-site": "cross-site",
					},
				),
				context("audit", path.split("/")),
			);

			expect(res.status).toBe(403);
			expect(fetchMock).not.toHaveBeenCalled();
		},
	);

	it("rejects a request whose Origin is a different origin", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		const res = await POST(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "POST", {
				origin: "http://evil.example",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(403);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("allows an explicit same-origin request through", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		const res = await GET(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "GET", {
				authorization: "Bearer caller-token",
				origin: "http://frontend",
				"sec-fetch-site": "same-origin",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	// Behind a TLS-terminating ingress the pod sees plaintext http and its own
	// host, while the browser's Origin is the public https origin. A same-origin
	// write (every Connect call and chat turn is a POST) must still pass — on the
	// strength of the CONFIGURED origin, which is the only thing that knows what
	// the browser sees.
	it("allows a same-origin POST behind a TLS-terminating ingress", async () => {
		withGateway();
		withTrustContext("https://app.example.com");
		registerAudit();

		const res = await POST(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "POST", {
				authorization: "Bearer caller-token",
				origin: "https://app.example.com",
				"sec-fetch-site": "same-origin",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("rejects a cross-site Origin behind a TLS-terminating ingress", async () => {
		withGateway();
		withTrustContext("https://app.example.com");
		registerAudit();

		const res = await POST(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "POST", {
				origin: "https://evil.example",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(403);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	// A comparison is only a check when one side is not the caller's to choose: the
	// origin compared against is the configured one, so supplying forwarding headers
	// alongside an Origin changes nothing about the answer.
	it("rejects a forged cross-site request that supplies its own forwarded host", async () => {
		withGateway();
		withTrustContext("https://app.example.com");
		registerAudit();

		const res = await POST(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "POST", {
				origin: "https://evil.example",
				"x-forwarded-proto": "https",
				"x-forwarded-host": "evil.example",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(403);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	// And with no configured origin there is nothing to compare against, so the
	// route refuses rather than admitting on an absent comparison.
	it("refuses every request when the deployment has no configured public origin", async () => {
		withGateway();
		withTrustContext();
		getWorkspaceConfiguration.mockReturnValue(undefined);
		registerAudit();

		const res = await POST(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "POST", {
				authorization: "Bearer caller-token",
				"sec-fetch-site": "same-origin",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(403);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("never leaks arbitrary caller headers to the upstream", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: {
					authorization: "Bearer caller-token",
					"x-secret-smuggle": "leak",
					"x-forwarded-host": "app.example",
				},
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.has("x-secret-smuggle")).toBe(false);
		// x-forwarded-host decides nothing here any more, and is never relayed to the
		// upstream as a raw header either.
		expect(forwarded.has("x-forwarded-host")).toBe(false);
	});

	it("replaces a caller-supplied internal token with the trusted one", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: {
					authorization: "Bearer caller-token",
					"x-codefly-internal-token": "cluster-secret",
				},
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.get("x-codefly-internal-token")).toBe(INTERNAL_TOKEN);
	});

	it("attaches the first-party trust headers resolved from config", async () => {
		withGateway();
		withTrustContext("https://app.example");
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: {
					authorization: "Bearer caller-token",
					// Offered by the caller and ignored: the stamped origin is the
					// configured one, whatever the request says.
					"x-forwarded-proto": "https",
					"x-forwarded-host": "evil.example",
				},
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.get("x-codefly-internal-token")).toBe(INTERNAL_TOKEN);
		expect(forwarded.get("x-codefly-public-origin")).toBe(
			"https://app.example",
		);
	});

	it.each(["records", "saas.accounts.v1.DatasourceService/ListSources"])(
		"forwards the session cookie so a cookie-authenticated remote is identified (%s)",
		async (path) => {
			withGateway();
			withTrustContext();
			registerAudit();

			await GET(
				rawRequest(`http://frontend/api/solutions/audit/proxy/${path}`, "GET", {
					cookie: "codefly_session=1; codefly_refresh=abc",
				}),
				context("audit", path.split("/")),
			);

			const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
			expect(forwarded.get("cookie")).toBe(
				"codefly_session=1; codefly_refresh=abc",
			);
		},
	);

	it("omits trust headers when no internal token is configured", async () => {
		withGateway();
		// No withTrustContext(): getWorkspaceSecret returns undefined.
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.has("x-codefly-internal-token")).toBe(false);
		expect(forwarded.has("x-codefly-public-origin")).toBe(false);
	});

	it("percent-encodes each path segment so it cannot smuggle a query or slash", async () => {
		withGateway();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/x", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["a b", "c?d=e"]),
		);

		const [target] = fetchMock.mock.calls[0];
		expect(target).toBe(
			`${GATEWAY_BASE}/v1/audit-backend/a%20b/c%3Fd%3De`,
		);
	});

	it("appends the original request query string verbatim", async () => {
		withGateway();
		registerAudit();

		await GET(
			proxyRequest(
				"http://frontend/api/solutions/audit/proxy/records?limit=10&cursor=abc",
				{ headers: { authorization: "Bearer caller-token" } },
			),
			context("audit", ["records"]),
		);

		const [target] = fetchMock.mock.calls[0];
		expect(target).toBe(
			`${GATEWAY_BASE}/v1/audit-backend/records?limit=10&cursor=abc`,
		);
	});

	it.each(["records", "saas.accounts.v1.DatasourceService/ListSources"])(
		"rejects an unregistered solution id without reaching any upstream (%s)",
		async (path) => {
			withGateway();
			// No registration for "ghost".

			const res = await GET(
				proxyRequest(`http://frontend/api/solutions/ghost/proxy/${path}`, {
					headers: { authorization: "Bearer caller-token" },
				}),
				context("ghost", path.split("/")),
			);

			expect(res.status).toBe(404);
			expect(fetchMock).not.toHaveBeenCalled();
		},
	);

	it("fails with 502 when the gateway endpoint is unresolvable", async () => {
		getEndpoints.mockReturnValue([]);
		registerAudit();

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(502);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("forwards a POST body to the upstream", async () => {
		withGateway();
		registerAudit();

		await POST(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				method: "POST",
				headers: {
					authorization: "Bearer caller-token",
					"content-type": "application/json",
				},
				body: JSON.stringify({ note: "hi" }),
			}),
			context("audit", ["records"]),
		);

		const init = fetchMock.mock.calls[0][1];
		expect(init.method).toBe("POST");
		expect(init.body).toBeInstanceOf(ArrayBuffer);
		expect(new TextDecoder().decode(init.body as ArrayBuffer)).toBe(
			JSON.stringify({ note: "hi" }),
		);
	});

	it("passes the upstream content-type through but not other upstream headers", async () => {
		withGateway();
		registerAudit();
		fetchMock.mockResolvedValueOnce(
			new Response("secret-body", {
				status: 201,
				headers: {
					"content-type": "application/json",
					"set-cookie": "upstream=leak",
					"x-internal-trace": "abc",
				},
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(201);
		expect(res.headers.get("content-type")).toBe("application/json");
		expect(res.headers.get("set-cookie")).toBeNull();
		expect(res.headers.get("x-internal-trace")).toBeNull();
	});

	// A1007B-01. This route is the only public way to a solution's backend, so a
	// challenge it drops is a challenge nobody can read: an MCP client sees a
	// bare 401 and reports "unauthorized" with nowhere to go. Probed on a live
	// cell before this fix: `POST /api/solutions/<id>/proxy/mcp` answered 401
	// with NO WWW-Authenticate, while the gateway had emitted one.
	it("passes the gateway's authentication challenge through on a 401", async () => {
		withGateway();
		registerAudit();
		const challenge =
			'Bearer error="invalid_token", ' +
			'resource_metadata="https://host.example.com/api/solutions/audit/proxy/.well-known/oauth-protected-resource"';
		fetchMock.mockResolvedValueOnce(
			new Response("authentication required", {
				status: 401,
				headers: {
					"content-type": "text/plain",
					"www-authenticate": challenge,
					// Still not forwarded: the allowlist stays an allowlist.
					"set-cookie": "upstream=leak",
				},
			}),
		);

		const res = await POST(
			proxyRequest("http://frontend/api/solutions/audit/proxy/mcp", {
				method: "POST",
			}),
			context("audit", ["mcp"]),
		);

		expect(res.status).toBe(401);
		expect(res.headers.get("www-authenticate")).toBe(challenge);
		expect(res.headers.get("set-cookie")).toBeNull();
	});

	// And nothing invents one: a response the gateway sent without a challenge
	// must not acquire one here, or a client would chase metadata for a refusal
	// that was never about its credential.
	it("adds no challenge the gateway did not send", async () => {
		withGateway();
		registerAudit();
		fetchMock.mockResolvedValueOnce(new Response("forbidden", { status: 403 }));

		const res = await POST(
			proxyRequest("http://frontend/api/solutions/audit/proxy/mcp", {
				method: "POST",
			}),
			context("audit", ["mcp"]),
		);

		expect(res.status).toBe(403);
		expect(res.headers.get("www-authenticate")).toBeNull();
	});

	it("returns 502 when the resolved gateway is unreachable", async () => {
		withGateway();
		registerAudit();
		fetchMock.mockRejectedValueOnce(new TypeError("fetch failed"));

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(502);
		expect(await res.text()).toBe("solution gateway unreachable");
	});

	it("categorizes a forwarded auth failure and correlates it via request id", async () => {
		withGateway();
		registerAudit();
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		fetchMock.mockResolvedValueOnce(
			new Response("unauthorized", {
				status: 401,
				headers: {
					"content-type": "application/json",
					"x-request-id": "req-42",
				},
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		// Status and body pass through untouched; the host adds the category and
		// the correlation id so an operator can tell auth from an upstream fault.
		expect(res.status).toBe(401);
		expect(res.headers.get("x-codefly-solution-error")).toBe("auth");
		expect(res.headers.get("x-request-id")).toBe("req-42");
		// A client/auth 4xx is a warning, never an error-level log line.
		expect(warn).toHaveBeenCalledTimes(1);
		expect(error).not.toHaveBeenCalled();
		// The line has to name the call. A refusal logged as the solution id and
		// a status and nothing else is indistinguishable between an expired token
		// on a poll and a procedure this deployment never routed anywhere — which
		// is exactly the confusion a run of these lines caused on a cell, where
		// every other hop logged nothing at all.
		const line = String(warn.mock.calls[0]?.[0]);
		expect(line).toContain("path=records");
		expect(line).toContain("method=GET");
		expect(line).toContain("status=401");
		expect(line).toContain("solution=audit");
		warn.mockRestore();
		error.mockRestore();
	});

	it("labels a forwarded 404 as not_found, not a registration miss", async () => {
		// The solution IS registered (it reached upstream); a 404 here is the
		// solution's own API reporting a missing resource, so it must not read as
		// "not_registered" and send an operator chasing a registration bug.
		withGateway();
		registerAudit();
		fetchMock.mockResolvedValueOnce(
			new Response("no such record", {
				status: 404,
				headers: { "content-type": "application/json" },
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records/999", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records", "999"]),
		);

		expect(res.status).toBe(404);
		expect(res.headers.get("x-codefly-solution-error")).toBe("not_found");
	});

	it("categorizes a forwarded solution-upstream failure", async () => {
		withGateway();
		registerAudit();
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		fetchMock.mockResolvedValueOnce(
			new Response("boom", {
				status: 503,
				headers: { "content-type": "application/json" },
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(503);
		expect(res.headers.get("x-codefly-solution-error")).toBe("upstream");
		// A solution 5xx is a real server fault and warrants error-level attention.
		expect(error).toHaveBeenCalledTimes(1);
		expect(warn).not.toHaveBeenCalled();
		warn.mockRestore();
		error.mockRestore();
	});
});

describe("solution proxy trace context", () => {
	const TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";
	const PARENT_ID = "00f067aa0ba902b7";
	const TRACEPARENT = `00-${TRACE_ID}-${PARENT_ID}-01`;
	// The route decides the forwarding policy from whether the caller presented
	// a credential the gateway will verify, so every case states which it is.
	const CREDENTIAL = { authorization: "Bearer caller-access-token" } as const;

	async function forwarded(headers: Record<string, string>): Promise<Headers> {
		withGateway();
		registerAudit();
		const res = await GET(
			rawRequest(
				"http://frontend/api/solutions/audit/proxy/records",
				"GET",
				headers,
			),
			context("audit", ["records"]),
		);
		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
		return fetchMock.mock.calls[0][1].headers as Headers;
	}

	it("forwards a credentialled caller's trace context, re-serialized", async () => {
		const headers = await forwarded({
			...CREDENTIAL,
			traceparent: TRACEPARENT,
			tracestate: " vendor=abc ,, other@sys=x y\t",
		});

		expect(headers.get("traceparent")).toBe(TRACEPARENT);
		expect(headers.get("tracestate")).toBe("vendor=abc,other@sys=x y");
	});

	it("forwards the trace context of a caller holding only a session cookie", async () => {
		const headers = await forwarded({
			cookie: "codefly_session=present",
			traceparent: TRACEPARENT,
			tracestate: "vendor=abc",
		});

		expect(headers.get("traceparent")).toBe(TRACEPARENT);
		expect(headers.get("tracestate")).toBe("vendor=abc");
	});

	// This route admits a request with neither Origin nor Sec-Fetch-Site and
	// serves a solution's `.well-known` anonymously, on a request it has already
	// stamped with the internal trust token. OpenTelemetry's default ParentBased
	// sampler obeys an incoming sampled bit, so honouring one from a caller with
	// no credential at all would let a stranger force 100% recording and export
	// on the gateway and on every solution behind it.
	it("links an anonymous caller's trace but strips its sampling decision", async () => {
		const headers = await forwarded({
			traceparent: TRACEPARENT,
			tracestate: "ot=p:8;r:62,vendor=abc",
		});

		expect(headers.get("traceparent")).toBe(`00-${TRACE_ID}-${PARENT_ID}-00`);
		// Vendors re-state the sampling decision in tracestate, so forwarding it
		// would hand back what clearing the sampled bit just took away.
		expect(headers.has("tracestate")).toBe(false);
	});

	it("keeps an anonymous caller's other flag bits while clearing sampled", async () => {
		const headers = await forwarded({
			traceparent: `00-${TRACE_ID}-${PARENT_ID}-03`,
		});

		expect(headers.get("traceparent")).toBe(`00-${TRACE_ID}-${PARENT_ID}-02`);
	});

	// A relay starts no span and assigns no parent id, so it has nothing to
	// advertise: a later version travels as sent rather than being rewritten as
	// version 00, which would assert a context nobody produced and destroy flag
	// bits a newer upstream understands.
	it.each([
		["extra fields", `cc-${TRACE_ID}-${PARENT_ID}-03-future-field`],
		["no extra fields", `cc-${TRACE_ID}-${PARENT_ID}-03`],
	])(
		"relays a later-version traceparent with %s unchanged",
		async (_, value) => {
			const headers = await forwarded({ ...CREDENTIAL, traceparent: value });

			expect(headers.get("traceparent")).toBe(value);
		},
	);

	it.each([
		["malformed", "not-a-traceparent"],
		["zero trace id", `00-${"0".repeat(32)}-${PARENT_ID}-01`],
		["two values", `${TRACEPARENT}, 00-${TRACE_ID}-${PARENT_ID}-00`],
	])("forwards no trace context for a %s traceparent", async (_, value) => {
		const headers = await forwarded({
			...CREDENTIAL,
			traceparent: value,
			tracestate: "vendor=abc",
		});

		expect(headers.has("traceparent")).toBe(false);
		expect(headers.has("tracestate")).toBe(false);
	});

	it("drops tracestate without a traceparent", async () => {
		const headers = await forwarded({
			...CREDENTIAL,
			tracestate: "vendor=abc",
		});

		expect(headers.has("traceparent")).toBe(false);
		expect(headers.has("tracestate")).toBe(false);
	});

	it("merges multiple tracestate header values with a comma", async () => {
		const incoming = new Headers(CREDENTIAL);
		incoming.set("traceparent", TRACEPARENT);
		incoming.append("tracestate", "a=1");
		incoming.append("tracestate", "b=2,c=3");
		withGateway();
		registerAudit();

		await GET(
			{
				method: "GET",
				url: "http://frontend/api/solutions/audit/proxy/records",
				headers: incoming,
				arrayBuffer: async () => new ArrayBuffer(0),
			} as unknown as Request,
			context("audit", ["records"]),
		);

		const headers = fetchMock.mock.calls[0][1].headers as Headers;
		expect(headers.get("traceparent")).toBe(TRACEPARENT);
		expect(headers.get("tracestate")).toBe("a=1,b=2,c=3");
	});

	it("truncates an over-long tracestate rather than dropping it", async () => {
		const members = Array.from({ length: 33 }, (_, i) => `k${i}=v`);
		const headers = await forwarded({
			...CREDENTIAL,
			traceparent: TRACEPARENT,
			tracestate: members.join(","),
		});

		expect(headers.get("traceparent")).toBe(TRACEPARENT);
		expect(headers.get("tracestate")).toBe(members.slice(0, 32).join(","));
	});

	it("drops all of a tracestate whose member does not parse", async () => {
		const headers = await forwarded({
			...CREDENTIAL,
			traceparent: TRACEPARENT,
			tracestate: "a=1,b=2,a=3",
		});

		expect(headers.get("traceparent")).toBe(TRACEPARENT);
		expect(headers.has("tracestate")).toBe(false);
	});

	it("never forwards baggage or other caller headers", async () => {
		const headers = await forwarded({
			...CREDENTIAL,
			traceparent: TRACEPARENT,
			baggage: "userId=alice",
			"x-forwarded-for": "10.0.0.1",
			"x-b3-traceid": TRACE_ID,
		});

		expect(headers.get("traceparent")).toBe(TRACEPARENT);
		expect(headers.has("baggage")).toBe(false);
		expect(headers.has("x-forwarded-for")).toBe(false);
		expect(headers.has("x-b3-traceid")).toBe(false);
	});
});


describe("module and solution data routes", () => {
 // The runtime serves a module page and a solution alike: public assets and
 // `.well-known` at its root, every other route under /v1/<alias> with the
 // person's Work Context. The gateway's /solutions/<alias>/ data route strips
 // that prefix, so a solution's data request sent there reached no route.
 it.each(["module", "solution"] as const)("routes a %s's data and assets through their owning gateway surfaces", async (kind) => {
  for (const [method, path, prefix] of [
   ["GET", "records", "v1"],
   ["GET", "settings/upload", "v1"],
   ["POST", "modules/store/store.v1.StoreService/ListItems", "v1"],
   ["GET", "assets/mf-manifest.json", "solutions"],
   ["HEAD", "assets/remoteEntry.js", "solutions"],
   ["GET", ".well-known/capabilities", "solutions"],
   // Only a read of a public path is public: a write under it is data.
   ["POST", "assets/upload", "v1"],
  ] as const) {
   withGateway();
   registerAudit("shared-example", kind);
   fetchMock.mockClear();
   const handler = method === "POST" ? POST : GET;
   const res = await handler(proxyRequest(`http://frontend/api/solutions/audit/proxy/${path}`, {
    method,
    headers: { authorization: "Bearer caller-token" },
    ...(method === "POST" ? { body: "{}" } : {}),
   }), context("audit", path.split("/")));
   expect(res.status).toBe(200);
   expect(fetchMock.mock.calls[0][0]).toBe(`${GATEWAY_BASE}/${prefix}/shared-example/${path}`);
   expect((fetchMock.mock.calls[0][1].headers as Headers).get("authorization")).toBe("Bearer caller-token");
  }
 });
 it("preserves a live installation refusal from the module gateway", async () => {
  withGateway();
  registerAudit("shared-example", "module");
  fetchMock.mockResolvedValue(new Response("installation revoked", {status: 403}));
  const res = await GET(proxyRequest("http://frontend/api/solutions/audit/proxy/records"), context("audit", ["records"]));
  expect(res.status).toBe(403);
  expect(await res.text()).toBe("installation revoked");
  expect(fetchMock.mock.calls[0][0]).toBe(`${GATEWAY_BASE}/v1/shared-example/records`);
 });
});

describe("versioned frontend assets", () => {
	it("asks the gateway whether an older asset generation is retained", async () => {
		withGateway();
		registerAudit();
		findSolution.mockResolvedValue({
			id: "audit",
			assetVersion: "b".repeat(64),
			backend: { serviceAlias: "audit" },
		});
		const res = await GET(
			proxyRequest("http://frontend/assets/mf-manifest.json"),
			{
				params: Promise.resolve({
					id: "audit",
					version: "a".repeat(64),
					path: ["assets", "mf-manifest.json"],
				}),
			},
		);
		expect(res.status).toBe(200);
		expect((fetchMock.mock.calls[0][1].headers as Headers).get("X-Codefly-Frontend-Version")).toBe("a".repeat(64));
	});
	it("asks the gateway to compare the generation again beside route resolution", async () => {
		withGateway();
		const version = "a".repeat(64);
		findSolution.mockResolvedValue({
			id: "audit",
			assetVersion: version,
			backend: { serviceAlias: "audit" },
		});
		await GET(
			proxyRequest("http://frontend/assets/remoteEntry.js", {
				headers: { "X-Codefly-Frontend-Version": "forged" },
			}),
			{
				params: Promise.resolve({
					id: "audit",
					version,
					path: ["assets", "remoteEntry.js"],
				}),
			},
		);
		expect(
			(fetchMock.mock.calls[0][1].headers as Headers).get(
				"X-Codefly-Frontend-Version",
			),
		).toBe(version);
	});
});

describe("retained frontend authority", () => {
 it.each([200, 410, 502, 503])("keeps gateway status %s with a headless current generation", async (status) => {
  withGateway();
  findSolution.mockResolvedValue(null);
  const cache = status === 200 ? "public, max-age=31536000, immutable" : "no-store";
  fetchMock.mockResolvedValue(new Response("retained response", {status, headers: {"cache-control": cache}}));
  const res = await GET(proxyRequest("http://frontend/assets/lazy.js"), {params: Promise.resolve({id: "audit", version: "a".repeat(64), path: ["assets", "lazy.js"]})});
  expect(res.status).toBe(status);
  expect(await res.text()).toBe("retained response");
  expect(res.headers.get("cache-control")).toBe(cache);
  expect(findSolution).not.toHaveBeenCalled();
  expect(fetchMock.mock.calls[0][0]).toBe(`${GATEWAY_BASE}/solutions/audit/assets/lazy.js`);
 });
 it("refuses malformed version before asking the gateway", async () => {
  withGateway();
  const res = await GET(proxyRequest("http://frontend/assets/lazy.js"), {params: Promise.resolve({id: "audit", version: "bad", path: ["assets", "lazy.js"]})});
  expect(res.status).toBe(410);
  expect(fetchMock).not.toHaveBeenCalled();
 });
 it("does not use retained code identity to open data routes", async () => {
  withGateway();
  findSolution.mockResolvedValue({id: "audit", assetVersion: "b".repeat(64), backend: {serviceAlias: "audit"}});
  const res = await GET(proxyRequest("http://frontend/records"), {params: Promise.resolve({id: "audit", version: "a".repeat(64), path: ["records"]})});
  expect(res.status).toBe(410);
  expect(fetchMock).not.toHaveBeenCalled();
 });
});

describe("the dedicated browser module edge", () => {
	it.each([
		["_browser", "..", "solutions", "_registry"],
		["_browser", "example.v1.Service", "Read", "..", "..", "solutions", "_registry"],
		["_browser", "example.v1.Service/Read"],
		["_browser", "example.v1.Service", "%2e%2e"],
	])("refuses malformed components before a trusted fetch: %j", async (...path) => {
		withGateway();
		withTrustContext();
		registerAudit();
		const res = await POST(proxyRequest("http://frontend/api/solutions/audit/proxy/_browser", { method: "POST" }), context("audit", path));
		expect(res.status).toBe(404);
		expect(fetchMock).not.toHaveBeenCalled();
	});
});


describe("generated clients through the browser module forwarder", () => {
	it("keeps the marker, generated procedure, base path and protocol headers", async () => {
		withGateway();
		withTrustContext();
		registerAudit();
		fetchMock.mockResolvedValue(Response.json({ scopes: [] }));
		const authedFetch: typeof fetch = async (input, init) => {
			const url = new URL(String(input), "http://frontend");
			const path = url.pathname.split("/proxy/")[1].split("/");
			return POST(new Request(url, init), context("audit", path));
		};
		const client = createClient(AccessibleScopeService, moduleTransport(
			requestBinding("/relay", () => "viewer", authedFetch, "/api/solutions/audit/proxy/_browser"),
		));
		await client.listMyAccessibleScopes({ orgId: "acme" });
		const [target, init] = fetchMock.mock.calls[0];
		expect(target).toBe(`${GATEWAY_BASE}/_browser/saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes`);
		expect(init.headers.get("authorization")).toBe("Bearer viewer");
		expect(init.headers.get("connect-protocol-version")).toBe("1");
		expect(init.headers.get("X-Codefly-Internal-Token")).toBe(INTERNAL_TOKEN);
		expect(init.redirect).toBe("error");
		// This descriptor tests generic forwarding only: gateway tests separately
		// refuse host namespaces and admit only composed-module publications.
	});

	it.each([[401, "unauthenticated", Code.Unauthenticated], [403, "permission_denied", Code.PermissionDenied]] as const)("retains owner refusal %s", async (status, code, want) => {
		withGateway();
		registerAudit();
		fetchMock.mockResolvedValue(Response.json({ code, message: "owner refusal" }, { status }));
		const authedFetch: typeof fetch = (input, init) => POST(
			new Request(new URL(String(input), "http://frontend"), init),
			context("audit", ["_browser", "example.v1.Service", "Read"]),
		);
		await expect(createClient(AccessibleScopeService, moduleTransport(
			requestBinding("/relay", () => "viewer", authedFetch, "/api/solutions/audit/proxy/_browser"),
		)).listMyAccessibleScopes({})).rejects.toMatchObject({ code: want });
	});

	it("never serves a module call through a retained asset version", async () => {
		withGateway();
		registerAudit();
		const res = await POST(proxyRequest("http://frontend/api/solutions/audit/proxy/_browser", { method: "POST" }), {
			params: Promise.resolve({ id: "audit", path: ["_browser", "example.v1.Service", "Read"], version: "a".repeat(64) }),
		});
		expect(res.status).toBe(404);
		expect(fetchMock).not.toHaveBeenCalled();
	});
});
