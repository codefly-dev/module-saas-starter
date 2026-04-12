"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { PlatformRole, OrgRole } from "./admin-core";
import {
  decodeJWTPayload,
  extractRoles,
  detectImpersonation,
  getStoredRefreshToken,
  storeRefreshToken,
  clearRefreshToken,
  type ImpersonationInfo,
} from "./admin-core";

const API_BASE = process.env.NEXT_PUBLIC_BACKEND_URL || "http://localhost:8080";

interface AuthState {
  user: { id: string; email?: string } | null;
  accessToken: string | null;
  platformRole?: PlatformRole;
  orgRole?: OrgRole;
  impersonation: ImpersonationInfo;
  isLoading: boolean;
  isAuthenticated: boolean;
}

interface AuthContextType extends AuthState {
  login: (provider: string, providerId: string, email: string) => Promise<void>;
  logout: () => Promise<void>;
  getToken: () => string | null;
}

const AuthContext = createContext<AuthContextType | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({
    user: null,
    accessToken: null,
    impersonation: { isImpersonated: false, impersonatedBy: "" },
    isLoading: true,
    isAuthenticated: false,
  });

  const getToken = useCallback(() => state.accessToken, [state.accessToken]);

  const setTokens = useCallback(
    (accessToken: string, refreshToken: string, userId?: string) => {
      const { platformRole, orgRole } = extractRoles(accessToken);
      const impersonation = detectImpersonation(accessToken);
      storeRefreshToken(refreshToken);
      setState({
        user: { id: userId || decodeJWTPayload(accessToken).sub },
        accessToken,
        platformRole,
        orgRole,
        impersonation,
        isLoading: false,
        isAuthenticated: true,
      });
    },
    [],
  );

  const login = useCallback(
    async (provider: string, providerId: string, email: string) => {
      const res = await fetch(`${API_BASE}/v1/auth/authenticate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ provider, provider_id: providerId, provider_email: email }),
      });
      if (!res.ok) throw new Error("Authentication failed");
      const data = await res.json();
      setTokens(data.accessToken, data.refreshToken, data.user?.uuid);
    },
    [setTokens],
  );

  const logout = useCallback(async () => {
    const refreshToken = getStoredRefreshToken();
    if (refreshToken && state.accessToken) {
      await fetch(`${API_BASE}/v1/auth/logout`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${state.accessToken}` },
        body: JSON.stringify({ refresh_token: refreshToken }),
      }).catch(() => {});
    }
    clearRefreshToken();
    setState({ user: null, accessToken: null, impersonation: { isImpersonated: false, impersonatedBy: "" }, isLoading: false, isAuthenticated: false });
  }, [state.accessToken]);

  useEffect(() => {
    const refreshToken = getStoredRefreshToken();
    if (!refreshToken) {
      setState((s) => ({ ...s, isLoading: false }));
      return;
    }
    fetch(`${API_BASE}/v1/auth/refresh`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: refreshToken }),
    })
      .then((res) => { if (!res.ok) throw new Error(); return res.json(); })
      .then((data) => setTokens(data.accessToken, data.refreshToken))
      .catch(() => { clearRefreshToken(); setState((s) => ({ ...s, isLoading: false })); });
  }, [setTokens]);

  const value = useMemo<AuthContextType>(
    () => ({ ...state, login, logout, getToken }),
    [state, login, logout, getToken],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextType {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
