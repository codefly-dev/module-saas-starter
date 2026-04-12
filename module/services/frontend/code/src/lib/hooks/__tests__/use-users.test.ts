import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, it, expect } from "vitest";
import React from "react";
import {
  useUsers,
  useUser,
  useSuspendUser,
  useUnsuspendUser,
} from "../use-users";

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return ({ children }: { children: React.ReactNode }) =>
    React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("useUsers", () => {
  it("returns list of users matching a query", async () => {
    const { result } = renderHook(() => useUsers("test"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
    expect(Array.isArray(result.current.data)).toBe(true);
    expect(result.current.data!.length).toBe(2);
  });

  it("returns users for empty query", async () => {
    const { result } = renderHook(() => useUsers(""), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });
});

describe("useUser", () => {
  it("returns a single user by uuid", async () => {
    const { result } = renderHook(() => useUser("user-1"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("does not fetch when uuid is empty", () => {
    const { result } = renderHook(() => useUser(""), {
      wrapper: createWrapper(),
    });
    expect(result.current.fetchStatus).toBe("idle");
  });
});

describe("useSuspendUser", () => {
  it("suspends a user", async () => {
    const { result } = renderHook(() => useSuspendUser(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      userId: "user-1",
      reason: "Policy violation",
    });
    expect(response).toBeDefined();
  });
});

describe("useUnsuspendUser", () => {
  it("unsuspends a user", async () => {
    const { result } = renderHook(() => useUnsuspendUser(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync("user-1");
    expect(response).toBeDefined();
  });
});
