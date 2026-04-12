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
import { setToken as setConnectToken } from "./connect/token-store";

const API_BASE = process.env.NEXT_PUBLIC_BACKEND_URL || "http://localhost:8080";

// OAuth provider configuration — read from env at build time. Add new
// providers here as presets; the sign-in button takes a provider id.
//
// Required env per provider:
//   NEXT_PUBLIC_{PROVIDER}_AUTHORIZE_URL  — hosted authorize endpoint
//   NEXT_PUBLIC_{PROVIDER}_CLIENT_ID      — OAuth client id
// Optional:
//   NEXT_PUBLIC_{PROVIDER}_SCOPE          — space-separated scopes
//
// The client secret lives ONLY on the backend. Never put it in NEXT_PUBLIC_*.
export interface ProviderPreset {
  id: string;
  displayName: string;
  authorizeURL: string;
  clientID: string;
  scope: string;
}

function readProviderPreset(id: string, displayName: string): ProviderPreset | null {
  const upper = id.toUpperCase();
  const authorizeURL = process.env[`NEXT_PUBLIC_${upper}_AUTHORIZE_URL`] as string | undefined;
  const clientID = process.env[`NEXT_PUBLIC_${upper}_CLIENT_ID`] as string | undefined;
  if (!authorizeURL || !clientID) return null;
  return {
    id,
    displayName,
    authorizeURL,
    clientID,
    scope: (process.env[`NEXT_PUBLIC_${upper}_SCOPE`] as string | undefined) || "openid email profile",
  };
}

export function availableProviders(): ProviderPreset[] {
  const presets: ProviderPreset[] = [];
  const workos = readProviderPreset("workos", "WorkOS");
  if (workos) presets.push(workos);
  const google = readProviderPreset("google", "Google");
  if (google) presets.push(google);
  const auth0 = readProviderPreset("auth0", "Auth0");
  if (auth0) presets.push(auth0);
  return presets;
}

// Build the provider's authorize URL for the authorization-code flow.
// State is a random opaque value the callback page verifies on return.
export function buildAuthorizeURL(preset: ProviderPreset, redirectURI: string, state: string): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: preset.clientID,
    redirect_uri: redirectURI,
    scope: preset.scope,
    state,
  });
  const joiner = preset.authorizeURL.includes("?") ? "&" : "?";
  return `${preset.authorizeURL}${joiner}${params.toString()}`;
}

// Generates a cryptographically random state token for CSRF protection
// on the OAuth redirect. Stored in sessionStorage, verified on callback.
export function newState(): string {
  const bytes = new Uint8Array(16);
  if (typeof window !== "undefined" && window.crypto) {
    window.crypto.getRandomValues(bytes);
  }
  return Array.from(bytes).map((b) => b.toString(16).padStart(2, "0")).join("");
}

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
  // Dev / fixture path — caller supplies an already-trusted identity.
  // Used by the dev-admin fixture and local-only tooling. In production
  // the OAuth redirect flow (signInWith) is the only path.
  login: (provider: string, providerId: string, email: string) => Promise<void>;
  // Kicks off the OAuth authorization-code flow by redirecting the
  // browser to the provider's hosted login. The callback page completes
  // the handshake.
  signInWith: (providerID: string) => void;
  // Completes the OAuth flow from /auth/callback: POSTs the code to the
  // backend, stores the returned tokens, redirects to the post-login
  // destination (or "/").
  completeOAuth: (code: string, state: string) => Promise<void>;
  logout: () => Promise<void>;
  getToken: () => string | null;
}

const AuthContext = createContext<AuthContextType | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({
    user: null,
    accessToken: null,
    impersonation: { isImpersonating: false },
    isLoading: true,
    isAuthenticated: false,
  });

  const getToken = useCallback(() => state.accessToken, [state.accessToken]);

  const setTokens = useCallback(
    (accessToken: string, refreshToken: string, userId?: string) => {
      const { platformRole, orgRole } = extractRoles(accessToken);
      const impersonation = detectImpersonation(accessToken);
      storeRefreshToken(refreshToken);
      setConnectToken(accessToken);
      // Set a lightweight presence cookie so the Next middleware can
      // short-circuit the "redirect to /auth/login" UX path. The real
      // auth check is still the backend sidecar — this cookie's
      // contents are not trusted anywhere server-side.
      if (typeof document !== "undefined") {
        document.cookie = "codefly_session=1; path=/; SameSite=Lax";
      }
      setState({
        user: { id: userId || String(decodeJWTPayload(accessToken).sub ?? "") },
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

  // OAuth redirect kickoff. Generates a CSRF state, stores it in
  // sessionStorage, and sends the browser to the provider's hosted
  // login. The callback page will verify the state and POST the code
  // to the backend.
  const signInWith = useCallback((providerID: string) => {
    const presets = availableProviders();
    const preset = presets.find((p) => p.id === providerID);
    if (!preset) {
      throw new Error(`OAuth provider not configured: ${providerID}`);
    }
    const state = newState();
    const redirectURI = `${window.location.origin}/auth/callback`;
    sessionStorage.setItem(`oauth_state_${providerID}`, state);
    sessionStorage.setItem("oauth_provider", providerID);
    sessionStorage.setItem("oauth_redirect_uri", redirectURI);
    sessionStorage.setItem("post_login_destination", window.location.pathname + window.location.search);
    window.location.href = buildAuthorizeURL(preset, redirectURI, state);
  }, []);

  // OAuth callback completion. Verifies state, POSTs code to the
  // backend /v1/auth/authenticate endpoint, stores the returned
  // tokens, and navigates to the post-login destination.
  const completeOAuth = useCallback(
    async (code: string, state: string) => {
      const providerID = sessionStorage.getItem("oauth_provider");
      const redirectURI = sessionStorage.getItem("oauth_redirect_uri");
      if (!providerID || !redirectURI) {
        throw new Error("OAuth state missing — start sign-in again");
      }
      const expectedState = sessionStorage.getItem(`oauth_state_${providerID}`);
      if (!expectedState || expectedState !== state) {
        throw new Error("OAuth state mismatch — possible CSRF attack");
      }

      const res = await fetch(`${API_BASE}/v1/auth/authenticate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: providerID,
          profile: { code, redirect_uri: redirectURI },
        }),
      });
      if (!res.ok) {
        const body = await res.text().catch(() => "");
        throw new Error(`OAuth exchange failed: ${res.status} ${body}`);
      }
      const data = await res.json();
      setTokens(data.accessToken, data.refreshToken, data.user?.uuid);

      // Clean up the one-shot state tokens so a later refresh doesn't
      // accidentally replay them.
      sessionStorage.removeItem(`oauth_state_${providerID}`);
      sessionStorage.removeItem("oauth_provider");
      sessionStorage.removeItem("oauth_redirect_uri");

      const dest = sessionStorage.getItem("post_login_destination") || "/";
      sessionStorage.removeItem("post_login_destination");
      if (typeof window !== "undefined") {
        window.location.replace(dest);
      }
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
    setConnectToken(null);
    if (typeof document !== "undefined") {
      document.cookie = "codefly_session=; path=/; SameSite=Lax; Max-Age=0";
    }
    setState({ user: null, accessToken: null, impersonation: { isImpersonating: false }, isLoading: false, isAuthenticated: false });
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
    () => ({ ...state, login, signInWith, completeOAuth, logout, getToken }),
    [state, login, signInWith, completeOAuth, logout, getToken],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextType {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
