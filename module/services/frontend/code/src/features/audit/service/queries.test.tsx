import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { useAuditService } from "@/lib/hooks/use-api-client";
import { auditEventTypesQuery, useAuditLog } from "./queries";

vi.mock("@/lib/hooks/use-api-client", () => ({
	useAuditService: vi.fn(),
}));

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
						name: "auth.login",
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
				name: "auth.login",
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
