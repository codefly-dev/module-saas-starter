import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import React from "react";
import { describe, expect, it } from "vitest";
import {
	useFeatureFlags,
	useGrantPlatformRole,
	useImpersonateUser,
	usePlatformAdmins,
	useRevokePlatformRole,
	useUpsertFeatureFlag,
} from "../use-platform-admin";

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: React.ReactNode }) =>
		React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("usePlatformAdmins", () => {
	it("returns list of platform admins", async () => {
		const { result } = renderHook(() => usePlatformAdmins(), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toBeDefined();
		expect(Array.isArray(result.current.data)).toBe(true);
		expect(result.current.data!.length).toBeGreaterThan(0);
	});
});

describe("useGrantPlatformRole", () => {
	it("grants a platform role", async () => {
		const { result } = renderHook(() => useGrantPlatformRole(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync({
			userId: "user-1",
			platformRole: "super_admin",
		});
		expect(response).toBeDefined();
	});
});

describe("useRevokePlatformRole", () => {
	it("revokes a platform role", async () => {
		const { result } = renderHook(() => useRevokePlatformRole(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync("user-1");
		expect(response).toBeDefined();
	});
});

describe("useImpersonateUser", () => {
	it("impersonates a user", async () => {
		const { result } = renderHook(() => useImpersonateUser(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync("user-1");
		expect(response).toBeDefined();
	});
});

describe("useFeatureFlags", () => {
	it("returns list of feature flags", async () => {
		const { result } = renderHook(() => useFeatureFlags(), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toBeDefined();
		expect(Array.isArray(result.current.data)).toBe(true);
		expect(result.current.data!.length).toBeGreaterThan(0);
	});
});

describe("useUpsertFeatureFlag", () => {
	it("creates a feature flag", async () => {
		const { result } = renderHook(() => useUpsertFeatureFlag(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync({
			name: "new-feature",
			enabled: true,
			rolloutPercent: 50,
		});
		expect(response).toBeDefined();
	});

	it("creates a feature flag with target orgs", async () => {
		const { result } = renderHook(() => useUpsertFeatureFlag(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync({
			name: "org-feature",
			description: "Only for specific orgs",
			enabled: true,
			rolloutPercent: 100,
			targetOrgIds: ["org-1", "org-2"],
		});
		expect(response).toBeDefined();
	});
});
