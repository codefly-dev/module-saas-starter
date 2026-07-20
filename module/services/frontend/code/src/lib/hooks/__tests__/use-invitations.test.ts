import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import React from "react";
import { describe, expect, it } from "vitest";
import {
	useAcceptInvitation,
	useCreateInvitation,
	useInvitations,
	useRevokeInvitation,
} from "../use-invitations";

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: React.ReactNode }) =>
		React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("useInvitations", () => {
	it("returns list of invitations for an org", async () => {
		const { result } = renderHook(() => useInvitations("org-1"), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toBeDefined();
		expect(Array.isArray(result.current.data)).toBe(true);
		expect(result.current.data!.length).toBeGreaterThan(0);
	});

	it("does not fetch when orgId is null", () => {
		const { result } = renderHook(() => useInvitations(null), {
			wrapper: createWrapper(),
		});
		expect(result.current.fetchStatus).toBe("idle");
	});
});

describe("useCreateInvitation", () => {
	it("creates an invitation", async () => {
		const { result } = renderHook(() => useCreateInvitation(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync({
			orgId: "org-1",
			email: "new@test.com",
			role: "member",
		});
		expect(response).toBeDefined();
		expect(response.invitation).toBeDefined();
	});
});

describe("useAcceptInvitation", () => {
	it("accepts an invitation by token", async () => {
		const { result } = renderHook(() => useAcceptInvitation(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync("inv_test_token");
		expect(response).toBeDefined();
	});
});

describe("useRevokeInvitation", () => {
	it("revokes an invitation", async () => {
		const { result } = renderHook(() => useRevokeInvitation(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync("inv-1");
		expect(response).toBeDefined();
	});
});
