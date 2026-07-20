"use client";

import { createClient } from "@connectrpc/connect";
import { startAuthentication } from "@simplewebauthn/browser";
import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { AuthService } from "@/gen/saas/accounts/v1/authentication_pb";
import type { OrgRole, PlatformRole } from "./auth-session";
import {
	clearRefreshToken,
	decodeJWTPayload,
	detectImpersonation,
	extractSessionContext,
	getStoredUserEmail,
	type ImpersonationInfo,
	storeRefreshToken,
	storeUserEmail,
} from "./auth-session";
import { setToken as setConnectToken } from "./connect/token-store";
import { apiTransport } from "./connect/transport";

const authClient = createClient(AuthService, apiTransport);

// Browser REST is always same-origin. Next/gateway resolves the accounts
// service server-side from Codefly bindings (CONV-004).

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

function readProviderPreset(
	id: string,
	displayName: string,
): ProviderPreset | null {
	const upper = id.toUpperCase();
	const authorizeURL = process.env[`NEXT_PUBLIC_${upper}_AUTHORIZE_URL`] as
		| string
		| undefined;
	const clientID = process.env[`NEXT_PUBLIC_${upper}_CLIENT_ID`] as
		| string
		| undefined;
	if (!authorizeURL || !clientID) return null;
	return {
		id,
		displayName,
		authorizeURL,
		clientID,
		scope:
			(process.env[`NEXT_PUBLIC_${upper}_SCOPE`] as string | undefined) ||
			"openid email profile",
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
// State is the server-signed nonce from BeginOAuth. codeChallenge is the SHA-256 of the
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
	organizationId?: string;
	platformRole?: PlatformRole;
	orgRole?: OrgRole;
	impersonation: ImpersonationInfo;
	isLoading: boolean;
	isAuthenticated: boolean;
	mfaRequired?: boolean;
}

interface AuthContextType extends AuthState {
	// Dev / fixture path — caller supplies an already-trusted identity.
	// Used by the dev-admin fixture and local-only tooling. In production
	// the OAuth redirect flow (signInWith) is the only path.
	// Resolves true when a normal session was issued, false when the browser was
	// moved into the MFA challenge continuation.
	login: (
		provider: string,
		providerId: string,
		email: string,
	) => Promise<boolean>;
	// Kicks off the OAuth authorization-code flow by redirecting the
	// browser to the provider's hosted login. The callback page completes
	// the handshake. Async because we mint a server-signed state via
	// BeginOAuth before the redirect.
	signInWith: (providerID: string) => Promise<void>;
	// Completes the OAuth flow from /auth/callback: POSTs the code to the
	// backend, stores the returned tokens, redirects to the post-login
	// destination (or "/").
	completeOAuth: (code: string, state: string) => Promise<void>;
	// Completes the durable one-use login transaction created after primary
	// authentication. The opaque transaction stays in sessionStorage, never
	// in a URL, cookie, or long-lived browser storage.
	completeMFA: (code: string) => Promise<void>;
	completeMFAWithPasskey: () => Promise<void>;
	cancelMFA: () => void;
	// Stores tokens received from magic link verification. The caller is
	// responsible for redirecting after this call.
	setTokensFromMagicLink: (
		accessToken: string,
		refreshToken: string,
		userId?: string,
		mfaToken?: string,
	) => boolean;
	logout: () => Promise<void>;
	switchOrganization: (organizationId: string) => Promise<void>;
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

	const applyAccessToken = useCallback(
		(accessToken: string, userId?: string, email?: string) => {
			const { organizationId, platformRole, orgRole } =
				extractSessionContext(accessToken);
			const impersonation = detectImpersonation(accessToken);
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
				organizationId,
				platformRole,
				orgRole,
				impersonation,
				isLoading: false,
				isAuthenticated: true,
			});
		},
		[],
	);

	const setTokens = useCallback(
		(
			accessToken: string,
			refreshToken: string,
			userId?: string,
			email?: string,
		) => {
			storeRefreshToken(refreshToken);
			applyAccessToken(accessToken, userId, email);
		},
		[applyAccessToken],
	);

	// Serialize exchanges in the browser. If two UI actions are queued before
	// React can disable the selector, each request uses the token installed by
	// the previous exchange and the final selection wins deterministically.
	const organizationSwitchQueue = useRef<Promise<void>>(Promise.resolve());
	const switchOrganization = useCallback(
		(organizationId: string): Promise<void> => {
			if (!organizationId) {
				return Promise.reject(new Error("Organization is required"));
			}
			const operation = organizationSwitchQueue.current
				.catch(() => undefined)
				.then(async () => {
					const response = await authClient.switchOrganization({
						organizationId,
					});
					if (!response.accessToken) {
						throw new Error("Organization exchange returned no access token");
					}
					applyAccessToken(response.accessToken);
				});
			organizationSwitchQueue.current = operation;
			return operation;
		},
		[applyAccessToken],
	);

	const beginMFA = useCallback(
		(data: Record<string, unknown>, email?: string) => {
			const token = data.mfaToken;
			if (typeof token !== "string" || token.length < 32) {
				throw new Error(
					"Authentication required MFA but returned no login transaction",
				);
			}
			sessionStorage.setItem("codefly_mfa_login_token", token);
			const user = data.user as
				| { uuid?: string; primaryEmail?: string }
				| undefined;
			setState({
				user: user?.uuid
					? { id: user.uuid, email: user.primaryEmail ?? email }
					: null,
				accessToken: null,
				impersonation: { isImpersonating: false },
				isLoading: false,
				isAuthenticated: false,
				mfaRequired: true,
			});
			window.location.replace("/auth/mfa");
		},
		[],
	);

	const login = useCallback(
		async (provider: string, providerId: string, email: string) => {
			const res = await fetch("/v1/auth/authenticate", {
				method: "POST",
				credentials: "include", // receive the httpOnly refresh-token cookie
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({
					provider,
					device_info: navigator.userAgent.slice(0, 512),
					fixture: { token: providerId },
				}),
			});
			if (!res.ok) throw new Error("Authentication failed");
			const data = await res.json();
			if (data.mfaRequired) {
				beginMFA(data, data.user?.primaryEmail ?? email);
				return false;
			}
			setTokens(
				data.accessToken,
				data.refreshToken,
				data.user?.uuid,
				data.user?.primaryEmail ?? email,
			);
			return true;
		},
		[beginMFA, setTokens],
	);

	// OAuth redirect kickoff. Asks the backend for a server-signed state,
	// generates a PKCE verifier+challenge, and sends the browser to the
	// provider's hosted login. The callback page completes the handshake.
	//
	// Two-layer CSRF protection: server requires and validates the state
	// signature on callback, while the browser also checks sessionStorage,
	// and PKCE binds the code redemption to this specific browser session
	// (so a stolen code can't be redeemed elsewhere).
	//
	const signInWith = useCallback(async (providerID: string) => {
		const presets = availableProviders();
		const preset = presets.find((p) => p.id === providerID);
		if (!preset) {
			throw new Error(`OAuth provider not configured: ${providerID}`);
		}
		const redirectURI = `${window.location.origin}/auth/callback`;
		const pkce = await newPkce();

		const res = await fetch("/v1/auth/oauth/begin", {
			method: "POST",
			credentials: "include",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ provider: providerID, redirect_uri: redirectURI }),
		});
		if (!res.ok) throw new Error(`BeginOAuth failed: ${res.status}`);
		const data = await res.json();
		if (typeof data.state !== "string" || data.state.length === 0) {
			throw new Error("BeginOAuth returned no signed state");
		}
		const state = data.state;

		sessionStorage.setItem(`oauth_state_${providerID}`, state);
		sessionStorage.setItem(`oauth_pkce_${providerID}`, pkce.verifier);
		sessionStorage.setItem("oauth_provider", providerID);
		sessionStorage.setItem("oauth_redirect_uri", redirectURI);
		sessionStorage.setItem(
			"post_login_destination",
			window.location.pathname + window.location.search,
		);
		window.location.href = buildAuthorizeURL(
			preset,
			redirectURI,
			state,
			pkce.challenge,
		);
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
			const codeVerifier =
				sessionStorage.getItem(`oauth_pkce_${providerID}`) ?? "";

			const res = await fetch("/v1/auth/authenticate", {
				method: "POST",
				credentials: "include", // receive the httpOnly refresh-token cookie
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({
					provider: providerID,
					device_info: navigator.userAgent.slice(0, 512),
					oauth_code: {
						code,
						redirect_uri: redirectURI,
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

			// Clean up the one-shot state tokens so a later refresh doesn't
			// accidentally replay them.
			sessionStorage.removeItem(`oauth_state_${providerID}`);
			sessionStorage.removeItem(`oauth_pkce_${providerID}`);
			sessionStorage.removeItem("oauth_provider");
			sessionStorage.removeItem("oauth_redirect_uri");

			if (data.mfaRequired) {
				beginMFA(data, data.user?.primaryEmail);
				return;
			}
			setTokens(data.accessToken, data.refreshToken, data.user?.uuid);

			const dest = sessionStorage.getItem("post_login_destination") || "/";
			sessionStorage.removeItem("post_login_destination");
			if (typeof window !== "undefined") {
				window.location.replace(dest);
			}
		},
		[beginMFA, setTokens],
	);

	const completeMFA = useCallback(
		async (code: string) => {
			const token = sessionStorage.getItem("codefly_mfa_login_token");
			if (!token) throw new Error("MFA login expired — start sign-in again");
			const res = await fetch("/v1/auth/mfa/complete", {
				method: "POST",
				credentials: "include",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({ mfa_token: token, code }),
			});
			if (!res.ok) {
				throw new Error("That code was not accepted. Check it and try again.");
			}
			const data = await res.json();
			sessionStorage.removeItem("codefly_mfa_login_token");
			setTokens(
				data.accessToken,
				data.refreshToken,
				data.user?.uuid,
				data.user?.primaryEmail,
			);

			const dest = sessionStorage.getItem("post_login_destination") || "/";
			sessionStorage.removeItem("post_login_destination");
			window.location.replace(dest);
		},
		[setTokens],
	);

	const completeMFAWithPasskey = useCallback(async () => {
		const token = sessionStorage.getItem("codefly_mfa_login_token");
		if (!token) throw new Error("MFA login expired — start sign-in again");

		const beginResponse = await fetch("/v1/auth/mfa/webauthn/begin", {
			method: "POST",
			credentials: "include",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ mfa_token: token }),
		});
		if (!beginResponse.ok) {
			throw new Error("No passkey is available for this sign-in.");
		}
		const begin = await beginResponse.json();
		const ceremonyToken = begin.ceremonyToken ?? begin.ceremony_token;
		const optionsJSON =
			begin.publicKeyOptionsJson ?? begin.public_key_options_json;
		if (typeof ceremonyToken !== "string" || typeof optionsJSON !== "string") {
			throw new Error(
				"The passkey challenge was incomplete. Start sign-in again.",
			);
		}

		let credential: unknown;
		try {
			credential = await startAuthentication({
				optionsJSON: JSON.parse(optionsJSON),
			});
		} catch (error) {
			if (error instanceof DOMException && error.name === "NotAllowedError") {
				throw new Error("Passkey verification was cancelled or timed out.");
			}
			throw error;
		}

		const completeResponse = await fetch("/v1/auth/mfa/webauthn/complete", {
			method: "POST",
			credentials: "include",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({
				mfa_token: token,
				ceremony_token: ceremonyToken,
				credential_response_json: JSON.stringify(credential),
			}),
		});
		if (!completeResponse.ok) {
			throw new Error("That passkey was not accepted. Start sign-in again.");
		}
		const data = await completeResponse.json();
		sessionStorage.removeItem("codefly_mfa_login_token");
		setTokens(
			data.accessToken,
			data.refreshToken,
			data.user?.uuid,
			data.user?.primaryEmail,
		);

		const dest = sessionStorage.getItem("post_login_destination") || "/";
		sessionStorage.removeItem("post_login_destination");
		window.location.replace(dest);
	}, [setTokens]);

	const cancelMFA = useCallback(() => {
		sessionStorage.removeItem("codefly_mfa_login_token");
		sessionStorage.removeItem("post_login_destination");
		setState((current) => ({ ...current, user: null, mfaRequired: false }));
		window.location.replace("/auth/login");
	}, []);

	const logout = useCallback(async () => {
		// Always hit logout so the server clears the httpOnly refresh cookie, even
		// if we have no access token in memory. The refresh token is read from the
		// cookie server-side (no body token needed).
		await fetch("/v1/auth/logout", {
			method: "POST",
			credentials: "include",
			headers: {
				"Content-Type": "application/json",
				...(state.accessToken
					? { Authorization: `Bearer ${state.accessToken}` }
					: {}),
			},
			body: JSON.stringify({}),
		}).catch(() => {});
		clearRefreshToken();
		sessionStorage.removeItem("codefly_mfa_login_token");
		setConnectToken(null);
		if (typeof document !== "undefined") {
			document.cookie = "codefly_session=; path=/; SameSite=Lax; Max-Age=0";
		}
		setState({
			user: null,
			accessToken: null,
			impersonation: { isImpersonating: false },
			isLoading: false,
			isAuthenticated: false,
		});
	}, [state.accessToken]);

	useEffect(() => {
		// Always attempt a refresh on load: the refresh token lives in an httpOnly
		// cookie the browser sends automatically (credentials: "include"). If the
		// cookie is absent/expired the request fails and we land unauthenticated.
		// The body no longer carries the token — the backend reads it from the cookie.
		fetch("/v1/auth/refresh", {
			method: "POST",
			credentials: "include",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({}),
		})
			.then((res) => {
				if (!res.ok) throw new Error();
				return res.json();
			})
			.then((data) => setTokens(data.accessToken, data.refreshToken))
			.catch(() => {
				clearRefreshToken();
				setState((s) => ({ ...s, isLoading: false }));
			});
	}, [setTokens]);

	const setTokensFromMagicLink = useCallback(
		(
			accessToken: string,
			refreshToken: string,
			userId?: string,
			mfaToken?: string,
		) => {
			if (mfaToken) {
				beginMFA({ mfaToken, user: userId ? { uuid: userId } : undefined });
				return false;
			}
			setTokens(accessToken, refreshToken, userId);
			return true;
		},
		[beginMFA, setTokens],
	);

	const value = useMemo<AuthContextType>(
		() => ({
			...state,
			login,
			signInWith,
			completeOAuth,
			completeMFA,
			completeMFAWithPasskey,
			cancelMFA,
			setTokensFromMagicLink,
			logout,
			switchOrganization,
			getToken,
		}),
		[
			state,
			login,
			signInWith,
			completeOAuth,
			completeMFA,
			completeMFAWithPasskey,
			cancelMFA,
			setTokensFromMagicLink,
			logout,
			switchOrganization,
			getToken,
		],
	);

	return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextType {
	const ctx = useContext(AuthContext);
	if (!ctx) throw new Error("useAuth must be used within AuthProvider");
	return ctx;
}
