import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, it, expect } from "vitest";
import React from "react";
import {
  useOrganizations,
  useOrganization,
  useCreateOrganization,
  useOrgMembers,
  useAddOrgMember,
  useRemoveOrgMember,
} from "../use-organizations";

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

describe("useOrganization", () => {
  it("returns a single organization by id", async () => {
    const { result } = renderHook(() => useOrganization("org-1"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("does not fetch when id is empty", () => {
    const { result } = renderHook(() => useOrganization(""), {
      wrapper: createWrapper(),
    });
    expect(result.current.fetchStatus).toBe("idle");
  });
});

describe("useCreateOrganization", () => {
  it("creates an organization", async () => {
    const { result } = renderHook(() => useCreateOrganization(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync("Test Org");
    expect(response).toBeDefined();
    expect(response.organization).toBeDefined();
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

describe("useAddOrgMember", () => {
  it("adds a member to an org", async () => {
    const { result } = renderHook(() => useAddOrgMember(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      orgId: "org-1",
      userId: "user-1",
      role: 1,
    });
    expect(response).toBeDefined();
  });
});

describe("useRemoveOrgMember", () => {
  it("removes a member from an org", async () => {
    const { result } = renderHook(() => useRemoveOrgMember(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      orgId: "org-1",
      userId: "user-1",
    });
    expect(response).toBeDefined();
  });
});
