import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import React from "react";
import { describe, expect, it } from "vitest";
import { useOrganizations, useOrgMembers } from "../use-organizations";

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: React.ReactNode }) =>
		React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("useOrganizations", () => {
	it("returns list of organizations", async () => {
		const { result } = renderHook(() => useOrganizations(), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toBeDefined();
		expect(Array.isArray(result.current.data)).toBe(true);
		expect(result.current.data!.length).toBe(2);
	});
});

describe("useOrgMembers", () => {
	it("returns members for an org", async () => {
		const { result } = renderHook(() => useOrgMembers("org-1"), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toBeDefined();
		expect(Array.isArray(result.current.data)).toBe(true);
	});

	it("does not fetch when orgId is null", () => {
		const { result } = renderHook(() => useOrgMembers(null), {
			wrapper: createWrapper(),
		});
		expect(result.current.fetchStatus).toBe("idle");
	});
});
