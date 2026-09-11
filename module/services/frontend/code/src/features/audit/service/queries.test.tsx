import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import {
	useAuditService,
	usePrincipalService,
} from "@/lib/hooks/use-api-client";
import {
	auditEventTypesQuery,
	useAuditLog,
	usePrincipalDirectory,
} from "./queries";

vi.mock("@/lib/hooks/use-api-client", () => ({
	useAuditService: vi.fn(),
	usePrincipalService: vi.fn(),
}));

function wrapper() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: ReactNode }) => (
		<QueryClientProvider client={client}>{children}</QueryClientProvider>
	);
}

describe("auditEventTypesQuery", () => {
	// The projection MUST live in queryFn, not select: queryClient.fetchQuery
	// (react-query v5) ignores select, so an imperative reader — the authoring
	// surface via fetchQuery — resolves to whatever queryFn returns. If the
	// projection ever drifted back into select, queryFn would resolve to the raw
	// { types } response and this assertion would fail.
	it("projects the raw registry response inside queryFn", async () => {
		const svc = {
			listAuditEventTypes: vi.fn(async () => ({
				$typeName: "saas.accounts.v1.ListAuditEventTypesResponse",
				types: [
					{
						$typeName: "saas.accounts.v1.AuditEventType",
						name: "saas.auth.login",
						version: 1,
						category: "authentication",
						owner: "accounts",
						deprecated: false,
						description: "A user logged in.",
					},
				],
			})),
		} as unknown as Parameters<typeof auditEventTypesQuery>[0];

		const query = auditEventTypesQuery(svc);
		const result = await query.queryFn();

		expect(query.queryKey).toEqual(["audit-event-types"]);
		expect(result).toEqual([
			{
				name: "saas.auth.login",
				version: 1,
				category: "authentication",
				owner: "accounts",
				deprecated: false,
				description: "A user logged in.",
			},
		]);
	});
});

describe("useAuditLog", () => {
	it("does not query until its organization binding is resolved", () => {
		const queryAuditLog = vi.fn();
		vi.mocked(useAuditService).mockReturnValue({
			queryAuditLog,
		} as unknown as ReturnType<typeof useAuditService>);
		const client = new QueryClient();
		const wrapper = ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={client}>{children}</QueryClientProvider>
		);

		const { result } = renderHook(
			() => useAuditLog({ orgId: "" }, { enabled: false }),
			{ wrapper },
		);

		expect(result.current.fetchStatus).toBe("idle");
		expect(queryAuditLog).not.toHaveBeenCalled();
	});
});

describe("usePrincipalDirectory", () => {
	it("walks the cursor so a multi-page org resolves in one query", async () => {
		const listPrincipals = vi
			.fn()
			.mockResolvedValueOnce({
				principals: [{ id: "p-1", displayName: "Ada Lovelace" }],
				nextPageToken: "cursor-1",
			})
			.mockResolvedValueOnce({
				principals: [{ id: "p-2", displayName: "deploy-bot" }],
				nextPageToken: "",
			});
		vi.mocked(usePrincipalService).mockReturnValue({
			listPrincipals,
		} as unknown as ReturnType<typeof usePrincipalService>);

		const { result } = renderHook(() => usePrincipalDirectory("org-1"), {
			wrapper: wrapper(),
		});

		await waitFor(() => expect(result.current.size).toBe(2));
		expect(result.current.get("p-1")).toBe("Ada Lovelace");
		expect(result.current.get("p-2")).toBe("deploy-bot");
		expect(listPrincipals).toHaveBeenNthCalledWith(1, {
			orgId: "org-1",
			pageSize: 200,
			pageToken: "",
		});
		expect(listPrincipals).toHaveBeenNthCalledWith(2, {
			orgId: "org-1",
			pageSize: 200,
			pageToken: "cursor-1",
		});
	});

	// The walk is bounded: an org past the ceiling leaves its oldest principals
	// unresolved (they render as truncated ids) rather than issuing an unbounded
	// fan of requests behind a page render.
	it("stops walking at the page ceiling", async () => {
		let page = 0;
		const listPrincipals = vi.fn(async () => {
			page += 1;
			return {
				principals: [{ id: `p-${page}`, displayName: `name-${page}` }],
				nextPageToken: `cursor-${page}`,
			};
		});
		vi.mocked(usePrincipalService).mockReturnValue({
			listPrincipals,
		} as unknown as ReturnType<typeof usePrincipalService>);

		const { result } = renderHook(() => usePrincipalDirectory("org-1"), {
			wrapper: wrapper(),
		});

		await waitFor(() => expect(result.current.size).toBe(5));
		expect(listPrincipals).toHaveBeenCalledTimes(5);
	});

	// ListPrincipals rejects an empty org_id, and the audit surfaces render
	// before the tenant binding resolves. An empty directory is a legible
	// intermediate state; a failed RPC is not.
	it("does not query until its organization binding is resolved", () => {
		const listPrincipals = vi.fn();
		vi.mocked(usePrincipalService).mockReturnValue({
			listPrincipals,
		} as unknown as ReturnType<typeof usePrincipalService>);

		const { result } = renderHook(() => usePrincipalDirectory(""), {
			wrapper: wrapper(),
		});

		expect(listPrincipals).not.toHaveBeenCalled();
		expect(result.current.size).toBe(0);
	});
});
