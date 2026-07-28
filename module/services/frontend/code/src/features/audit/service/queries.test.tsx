import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { useAuditService } from "@/lib/hooks/use-api-client";
import { useAuditLog } from "./queries";

vi.mock("@/lib/hooks/use-api-client", () => ({
	useAuditService: vi.fn(),
}));

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
