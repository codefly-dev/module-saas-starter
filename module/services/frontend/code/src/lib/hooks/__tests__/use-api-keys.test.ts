import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, it, expect } from "vitest";
import React from "react";
import {
  useAPIKeys,
  useCreateAPIKey,
  useRevokeAPIKey,
} from "../use-api-keys";

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return ({ children }: { children: React.ReactNode }) =>
    React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("useAPIKeys", () => {
  it("returns list of API keys for an org", async () => {
    const { result } = renderHook(() => useAPIKeys("org-1"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
    expect(Array.isArray(result.current.data)).toBe(true);
  });

  it("does not fetch when orgId is null", () => {
    const { result } = renderHook(() => useAPIKeys(null), {
      wrapper: createWrapper(),
    });
    expect(result.current.fetchStatus).toBe("idle");
  });
});

describe("useCreateAPIKey", () => {
  it("creates an API key", async () => {
    const { result } = renderHook(() => useCreateAPIKey(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      organizationId: "org-1",
      name: "Test Key",
    });
    expect(response).toBeDefined();
    expect(response.key).toBeDefined();
    expect(response.plaintextKey).toBeDefined();
  });

  it("creates an API key with scopes", async () => {
    const { result } = renderHook(() => useCreateAPIKey(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      organizationId: "org-1",
      name: "Scoped Key",
      scopes: [{ resource: "users", action: "read" }],
      environment: 1,
    });
    expect(response).toBeDefined();
  });
});

describe("useRevokeAPIKey", () => {
  it("revokes an API key", async () => {
    const { result } = renderHook(() => useRevokeAPIKey(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync("key-1");
    expect(response).toBeDefined();
  });
});
