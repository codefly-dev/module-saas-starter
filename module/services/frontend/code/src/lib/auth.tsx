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
  storeRefreshToken,
  clearRefreshToken,
  getStoredUserEmail,
  storeUserEmail,
  type ImpersonationInfo,
} from "./admin-core";
import { setToken as setConnectToken } from "./connect/token-store";

// API REST endpoint injected by codefly via NEXT_PUBLIC_API_REST.
// Falls back to direct API port for local dev without gateway.
const API_BASE =
  process.env.NEXT_PUBLIC_API_REST ||
  process.env.NEXT_PUBLIC_BACKEND_URL ||
  "http://localhost:5962";

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
// State is the server-signed nonce from BeginOAuth (or a client-only
// random fallback in offline dev). codeChallenge is the SHA-256 of the
// PKCE verifier — empty disables PKCE for callers that don't need it.
export function buildAuthorizeURL(
  preset: ProviderPreset,
  redirectURI: string,
  state: string,
  codeChallenge?: string,
): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: preset.clientID,
    redirect_uri: redirectURI,
    scope: preset.scope,
    state,
  });
  if (codeChallenge) {
    params.set("code_challenge", codeChallenge);
    params.set("code_challenge_method", "S256");
  }
  const joiner = preset.authorizeURL.includes("?") ? "&" : "?";
  return `${preset.authorizeURL}${joiner}${params.toString()}`;
}

// Generates a cryptographically random state token for CSRF protection
// on the OAuth redirect. Used as a fallback when the backend's BeginOAuth
// is unreachable (offline dev). Production flow prefers the
// server-signed state from BeginOAuth — see signInWith.
export function newState(): string {
  const bytes = new Uint8Array(16);
  if (typeof window !== "undefined" && window.crypto) {
    window.crypto.getRandomValues(bytes);
  }
  return Array.from(bytes).map((b) => b.toString(16).padStart(2, "0")).join("");
}

// PKCE: cryptographically random verifier + SHA-256 challenge.
// The verifier stays on the client (sessionStorage); the challenge is
// sent to the provider in the authorize URL. On the token-exchange
// hop we hand the verifier to the backend, which forwards it to the
// provider's token endpoint as `code_verifier`. The provider re-hashes
// it and compares — this binds the code redemption to the original
// authorize request and blocks a stolen-code replay.
async function newPkce(): Promise<{ verifier: string; challenge: string }> {
  // 64 bytes → 86 char base64url, well within the 43–128 RFC range.
  const bytes = new Uint8Array(64);
  if (typeof window !== "undefined" && window.crypto) {
    window.crypto.getRandomValues(bytes);
  }
  const verifier = base64urlEncode(bytes);
  const digest = await window.crypto.subtle.digest("SHA-256", bytes);
  const challenge = base64urlEncode(new Uint8Array(digest));
  return { verifier, challenge };
}

function base64urlEncode(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
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
  // the handshake. Async because we mint a server-signed state via
  // BeginOAuth before the redirect.
  signInWith: (providerID: string) => Promise<void>;
  // Completes the OAuth flow from /auth/callback: POSTs the code to the
  // backend, stores the returned tokens, redirects to the post-login
  // destination (or "/").
  completeOAuth: (code: string, state: string) => Promise<void>;
  // Stores tokens received from magic link verification. The caller is
  // responsible for redirecting after this call.
  setTokensFromMagicLink: (accessToken: string, refreshToken: string, userId?: string) => void;
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
    (accessToken: string, refreshToken: string, userId?: string, email?: string) => {
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
      const payload = decodeJWTPayload(accessToken);
      // Resolve email: explicit arg (login flow) > JWT email claim
      // (future-proofing) > previously-stored value (refresh-token
      // round-trip after page reload). Persist the resolved email so
      // the next reload finds it.
      const resolvedEmail =
        email ||
        (typeof payload.email === "string" ? payload.email : undefined) ||
        getStoredUserEmail() ||
        undefined;
      if (resolvedEmail) storeUserEmail(resolvedEmail);
      setState({
        user: {
          id: userId || String(payload.sub ?? ""),
          email: resolvedEmail,
        },
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
        credentials: "include", // receive the httpOnly refresh-token cookie
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ provider, provider_id: providerId, provider_email: email }),
      });
      if (!res.ok) throw new Error("Authentication failed");
      const data = await res.json();
      setTokens(data.accessToken, data.refreshToken, data.user?.uuid, email);
    },
    [setTokens],
  );

  // OAuth redirect kickoff. Asks the backend for a server-signed state,
  // generates a PKCE verifier+challenge, and sends the browser to the
  // provider's hosted login. The callback page completes the handshake.
  //
  // Two-layer CSRF protection: server validates state signature on
  // callback (defense in depth even if sessionStorage is compromised),
  // and PKCE binds the code redemption to this specific browser session
  // (so a stolen code can't be redeemed elsewhere).
  //
  // If BeginOAuth fails (backend down, dev disconnect), falls back to a
  // client-only random state — same security posture we had before this
  // RPC existed, so offline dev is not blocked.
  const signInWith = useCallback(async (providerID: string) => {
    const presets = availableProviders();
    const preset = presets.find((p) => p.id === providerID);
    if (!preset) {
      throw new Error(`OAuth provider not configured: ${providerID}`);
    }
    const redirectURI = `${window.location.origin}/auth/callback`;
    const pkce = await newPkce();

    // Try to mint a server-signed state. Fall back to client-only on
    // failure — the FE's existing sessionStorage check still applies.
    let state: string;
    try {
      const res = await fetch(`${API_BASE}/v1/auth/oauth/begin`, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ provider: providerID, redirect_uri: redirectURI }),
      });
      if (!res.ok) throw new Error(`BeginOAuth failed: ${res.status}`);
      const data = await res.json();
      state = data.state ?? newState();
    } catch {
      state = newState();
    }

    sessionStorage.setItem(`oauth_state_${providerID}`, state);
    sessionStorage.setItem(`oauth_pkce_${providerID}`, pkce.verifier);
    sessionStorage.setItem("oauth_provider", providerID);
    sessionStorage.setItem("oauth_redirect_uri", redirectURI);
    sessionStorage.setItem("post_login_destination", window.location.pathname + window.location.search);
    window.location.href = buildAuthorizeURL(preset, redirectURI, state, pkce.challenge);
  }, []);

  // OAuth callback completion. Verifies state client-side, then POSTs
  // {code, redirect_uri, state, code_verifier} to /v1/auth/authenticate.
  // Server independently re-validates state (signature + binding) and
  // forwards code_verifier to the provider's token endpoint for PKCE.
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
      const codeVerifier = sessionStorage.getItem(`oauth_pkce_${providerID}`) ?? "";

      const res = await fetch(`${API_BASE}/v1/auth/authenticate`, {
        method: "POST",
        credentials: "include", // receive the httpOnly refresh-token cookie
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: providerID,
          profile: {
            code,
            redirect_uri: redirectURI,
            // state is forwarded so the server can repeat the
            // sessionStorage-only check. The code_verifier is the PKCE
            // secret — server forwards it to the provider unchanged.
            state,
            code_verifier: codeVerifier,
          },
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
      sessionStorage.removeItem(`oauth_pkce_${providerID}`);
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
    // Always hit logout so the server clears the httpOnly refresh cookie, even
    // if we have no access token in memory. The refresh token is read from the
    // cookie server-side (no body token needed).
    await fetch(`${API_BASE}/v1/auth/logout`, {
      method: "POST",
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        ...(state.accessToken ? { Authorization: `Bearer ${state.accessToken}` } : {}),
      },
      body: JSON.stringify({}),
    }).catch(() => {});
    clearRefreshToken();
    setConnectToken(null);
    if (typeof document !== "undefined") {
      document.cookie = "codefly_session=; path=/; SameSite=Lax; Max-Age=0";
    }
    setState({ user: null, accessToken: null, impersonation: { isImpersonating: false }, isLoading: false, isAuthenticated: false });
  }, [state.accessToken]);

  useEffect(() => {
    // Always attempt a refresh on load: the refresh token lives in an httpOnly
    // cookie the browser sends automatically (credentials: "include"). If the
    // cookie is absent/expired the request fails and we land unauthenticated.
    // The body no longer carries the token — the backend reads it from the cookie.
    fetch(`${API_BASE}/v1/auth/refresh`, {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    })
      .then((res) => { if (!res.ok) throw new Error(); return res.json(); })
      .then((data) => setTokens(data.accessToken, data.refreshToken))
      .catch(() => { clearRefreshToken(); setState((s) => ({ ...s, isLoading: false })); });
  }, [setTokens]);

  const setTokensFromMagicLink = useCallback(
    (accessToken: string, refreshToken: string, userId?: string) => {
      setTokens(accessToken, refreshToken, userId);
    },
    [setTokens],
  );

  const value = useMemo<AuthContextType>(
    () => ({ ...state, login, signInWith, completeOAuth, setTokensFromMagicLink, logout, getToken }),
    [state, login, signInWith, completeOAuth, setTokensFromMagicLink, logout, getToken],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextType {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
