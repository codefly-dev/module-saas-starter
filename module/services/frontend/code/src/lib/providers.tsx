"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createContext, useContext, useState, type ReactNode } from "react";
import { AuthProvider } from "./auth";
import { adminConfig } from "./admin-config";
import type { AdminConfig } from "./admin-core";

const AdminConfigContext = createContext<AdminConfig>(adminConfig);

export function useAdminConfigContext(): AdminConfig {
  return useContext(AdminConfigContext);
}

export function Providers({ children }: { children: ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30 * 1000,
            retry: 1,
          },
        },
      }),
  );

  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <AdminConfigContext.Provider value={adminConfig}>
          {children}
        </AdminConfigContext.Provider>
      </AuthProvider>
    </QueryClientProvider>
  );
}
