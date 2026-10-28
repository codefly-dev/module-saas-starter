import { AccessibleScopeService } from "@codefly-dev/saas-sdk";
import { Code, createClient } from "@connectrpc/connect";
import { renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
	solutionTransport,
	moduleTransport,
	requestBinding,
	useAccessibleScope,
	viewerOrganization,
} from "../index.js";

function token(claims: Record<string, unknown>): string {
	return `header.${btoa(JSON.stringify(claims)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")}.signature`;
}

function body(init?: RequestInit): unknown {
	return JSON.parse(new TextDecoder().decode(init?.body as Uint8Array));
}

const scopes = (reply: Response) =>
	vi.fn<typeof fetch>(async () => reply.clone());

describe("solutionTransport", () => {
	it("reaches the host's procedures and a solution path through authedFetch, with the viewer's bearer", async () => {
		const authedFetch = scopes(Response.json({ scopes: [] }));
		const client = createClient(
			AccessibleScopeService,
			solutionTransport({
				apiBase: "/api/solutions/s/proxy",
				getAccessToken: () => "viewer",
				authedFetch,
			}),
		);
		await client.listMyAccessibleScopes({
			orgId: "o1",
			resourceType: "documents",
			action: "read",
		});
		const [url, init] = authedFetch.mock.calls[0]!;
		expect(String(url)).toBe(
			"/api/solutions/s/proxy/saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes",
		);
		expect(new Headers(init?.headers).get("authorization")).toBe(
			"Bearer viewer",
		);
		expect(body(init)).toEqual({
			orgId: "o1",
			resourceType: "documents",
			action: "read",
		});

		const moduleFetch = scopes(Response.json({ scopes: [] }));
		await createClient(
			AccessibleScopeService,
			solutionTransport(
				{
					apiBase: "/api",
					getAccessToken: () => null,
					authedFetch: moduleFetch,
				},
				"/modules/m",
			),
		).listMyAccessibleScopes({ orgId: "o1" });
		expect(String(moduleFetch.mock.calls[0]![0])).toBe(
			"/api/modules/m/saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes",
		);
	});

	it("reports a refused authority, and only that", async () => {
		const onUnauthorized = vi.fn();
		const denied = createClient(
			AccessibleScopeService,
			solutionTransport(
				{
					apiBase: "/api",
					getAccessToken: () => null,
					authedFetch: scopes(
						Response.json(
							{ code: "permission_denied", message: "no" },
							{ status: 403 },
						),
					),
				},
				"",
				onUnauthorized,
			),
		);
		await expect(
			denied.listMyAccessibleScopes({ orgId: "o1" }),
		).rejects.toMatchObject({ code: Code.PermissionDenied });
		const down = createClient(
			AccessibleScopeService,
			solutionTransport(
				{
					apiBase: "/api",
					getAccessToken: () => null,
					authedFetch: scopes(
						Response.json(
							{ code: "unavailable", message: "down" },
							{ status: 503 },
						),
					),
				},
				"",
				onUnauthorized,
			),
		);
		await expect(
			down.listMyAccessibleScopes({ orgId: "o1" }),
		).rejects.toMatchObject({ code: Code.Unavailable });
		expect(onUnauthorized).toHaveBeenCalledTimes(1);
	});
});

describe("viewerOrganization", () => {
	it("reads the org claim, and nothing from a token without one", () => {
		expect(viewerOrganization(token({ sub: "alice", org: "o1" }))).toBe("o1");
		expect(viewerOrganization(token({ sub: "alice" }))).toBe("");
		expect(viewerOrganization("opaque")).toBe("");
		expect(viewerOrganization(null)).toBe("");
	});
});

describe("useAccessibleScope", () => {
	it("says whether the viewer holds the action anywhere in the org", async () => {
		for (const [reply, want] of [
			[Response.json({ scopes: [{ nodeId: "n1", scopePath: "/a" }] }), "some"],
			[Response.json({}), "none"],
			[
				Response.json(
					{ code: "unavailable", message: "down" },
					{ status: 503 },
				),
				"error",
			],
		] as const) {
			const authedFetch = scopes(reply);
			const { result } = renderHook(() =>
				useAccessibleScope(
					{ apiBase: "/api", getAccessToken: () => null, authedFetch },
					"o1",
					"documents",
					"read",
				),
			);
			expect(result.current).toBe("loading");
			await waitFor(() => expect(result.current).toBe(want));
			expect(body(authedFetch.mock.calls[0]![1])).toEqual({
				orgId: "o1",
				resourceType: "documents",
				action: "read",
				pageSize: 1,
			});
		}
	});
});


describe("moduleTransport", () => {
	it("uses the explicitly supplied standalone root with the generated client", async () => {
		const authedFetch = scopes(Response.json({ scopes: [] }));
		await createClient(
			AccessibleScopeService,
			moduleTransport(requestBinding("/relay", () => "viewer", authedFetch, "/")),
		).listMyAccessibleScopes({});
		expect(String(authedFetch.mock.calls[0]![0])).toBe(
			"/saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes",
		);
	});
	it("preserves the explicit base when rebuilding credentials and uses the generated client unchanged", async () => {
		const authedFetch = scopes(Response.json({ scopes: [] }));
		const binding = requestBinding("/solution-relay", () => "viewer", authedFetch, "/api/solutions/example/proxy/_browser");
		await createClient(AccessibleScopeService, moduleTransport(binding)).listMyAccessibleScopes({ orgId: "acme" });
		expect(String(authedFetch.mock.calls[0]![0])).toBe("/api/solutions/example/proxy/_browser/saas.accounts.v1.AccessibleScopeService/ListMyAccessibleScopes");
		expect(new Headers(authedFetch.mock.calls[0]![1]?.headers).get("authorization")).toBe("Bearer viewer");
		expect(authedFetch.mock.calls[0]![1]?.credentials).toBe("same-origin");
		expect(binding.apiBase).toBe("/solution-relay");
	});
	it.each([undefined, "", "   "])("refuses absent connection %s before fetch", (moduleApiBase) => {
		const authedFetch = vi.fn();
		expect(() => moduleTransport({ apiBase: "/relay", moduleApiBase, authedFetch })).toThrow("module connection unavailable");
		expect(authedFetch).not.toHaveBeenCalled();
	});
	it.each([[401, "unauthenticated", Code.Unauthenticated], [403, "permission_denied", Code.PermissionDenied]] as const)("preserves refusal %s without changing its meaning", async (status, code, want) => {
		const onUnauthorized = vi.fn();
		const binding = { apiBase: "/relay", moduleApiBase: "/edge", authedFetch: scopes(Response.json({ code, message: "owner refusal" }, { status })) };
		await expect(createClient(AccessibleScopeService, moduleTransport(binding, onUnauthorized)).listMyAccessibleScopes({})).rejects.toMatchObject({ code: want });
		expect(onUnauthorized).toHaveBeenCalledOnce();
	});
});
