import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, it, expect } from "vitest";
import React from "react";
import {
  useRoles,
  useCreateRole,
  useDeleteRole,
  useAssignRole,
  useRevokeRole,
  useCheckPermission,
} from "../use-roles";

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return ({ children }: { children: React.ReactNode }) =>
    React.createElement(QueryClientProvider, { client: queryClient }, children);
}

describe("useRoles", () => {
  it("returns list of roles", async () => {
    const { result } = renderHook(() => useRoles(), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
    expect(Array.isArray(result.current.data)).toBe(true);
  });
});

describe("useCreateRole", () => {
  it("creates a role", async () => {
    const { result } = renderHook(() => useCreateRole(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({ name: "Editor" });
    expect(response).toBeDefined();
    expect(response.role).toBeDefined();
  });

  it("creates a role with permissions", async () => {
    const { result } = renderHook(() => useCreateRole(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      name: "Admin",
      description: "Full access",
      permissions: [{ resource: "org", action: "manage" }],
    });
    expect(response).toBeDefined();
  });
});

describe("useDeleteRole", () => {
  it("deletes a role", async () => {
    const { result } = renderHook(() => useDeleteRole(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync("role-1");
    expect(response).toBeDefined();
  });
});

describe("useAssignRole", () => {
  it("assigns a role to a subject", async () => {
    const { result } = renderHook(() => useAssignRole(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      subjectId: "user-1",
      roleId: "role-1",
    });
    expect(response).toBeDefined();
  });
});

describe("useRevokeRole", () => {
  it("revokes a role from a subject", async () => {
    const { result } = renderHook(() => useRevokeRole(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      subjectId: "user-1",
      roleId: "role-1",
    });
    expect(response).toBeDefined();
  });
});

describe("useCheckPermission", () => {
  it("checks a permission", async () => {
    const { result } = renderHook(() => useCheckPermission(), {
      wrapper: createWrapper(),
    });
    const response = await result.current.mutateAsync({
      subjectId: "user-1",
      resource: "org",
      action: "read",
    });
    expect(response).toBeDefined();
  });
});
