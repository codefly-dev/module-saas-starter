import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, it, expect } from "vitest";
import React from "react";
import { useAuditLog } from "../use-audit";

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return ({ children }: { children: React.ReactNode }) =>
    React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("useAuditLog", () => {
  it("returns audit events", async () => {
    const { result } = renderHook(() => useAuditLog({}), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
    expect(result.current.data!.events).toBeDefined();
    expect(Array.isArray(result.current.data!.events)).toBe(true);
    expect(result.current.data!.events.length).toBe(2);
    expect(result.current.data!.totalCount).toBe(2);
  });

  it("accepts filter parameters", async () => {
    const { result } = renderHook(
      () =>
        useAuditLog({
          orgId: "org-1",
          action: "user.registered",
          actorId: "actor-1",
          pageSize: 10,
        }),
      { wrapper: createWrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
    expect(result.current.data!.events).toBeDefined();
  });

  it("returns events with expected shape", async () => {
    const { result } = renderHook(() => useAuditLog({}), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    const event = result.current.data!.events[0];
    expect(event).toHaveProperty("id");
    expect(event).toHaveProperty("action");
    expect(event).toHaveProperty("actorId");
    expect(event).toHaveProperty("createdAt");
  });
});
