import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import React from "react";
import { describe, expect, it } from "vitest";
import {
	useAddTeamMember,
	useCreateTeam,
	useRemoveTeamMember,
	useTeamMembers,
	useTeams,
} from "../use-teams";

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return ({ children }: { children: React.ReactNode }) =>
		React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("useTeams", () => {
	it("returns list of teams for an org", async () => {
		const { result } = renderHook(() => useTeams("org-1"), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toBeDefined();
		expect(Array.isArray(result.current.data)).toBe(true);
	});

	it("does not fetch when orgId is null", () => {
		const { result } = renderHook(() => useTeams(null), {
			wrapper: createWrapper(),
		});
		expect(result.current.fetchStatus).toBe("idle");
	});
});

describe("useCreateTeam", () => {
	it("creates a team", async () => {
		const { result } = renderHook(() => useCreateTeam(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync({
			orgId: "org-1",
			name: "Engineering",
		});
		expect(response).toBeDefined();
		expect(response.team).toBeDefined();
	});
});

describe("useTeamMembers", () => {
	it("returns members for a team", async () => {
		const { result } = renderHook(() => useTeamMembers("team-1"), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(result.current.data).toBeDefined();
		expect(Array.isArray(result.current.data)).toBe(true);
	});

	it("does not fetch when teamId is null", () => {
		const { result } = renderHook(() => useTeamMembers(null), {
			wrapper: createWrapper(),
		});
		expect(result.current.fetchStatus).toBe("idle");
	});
});

describe("useAddTeamMember", () => {
	it("adds a member to a team", async () => {
		const { result } = renderHook(() => useAddTeamMember(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync({
			teamId: "team-1",
			userId: "user-1",
		});
		expect(response).toBeDefined();
	});
});

describe("useRemoveTeamMember", () => {
	it("removes a member from a team", async () => {
		const { result } = renderHook(() => useRemoveTeamMember(), {
			wrapper: createWrapper(),
		});
		const response = await result.current.mutateAsync({
			teamId: "team-1",
			userId: "user-1",
		});
		expect(response).toBeDefined();
	});
});
