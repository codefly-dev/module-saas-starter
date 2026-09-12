"use client";

import * as SaasSdk from "@codefly-dev/saas-sdk";
import * as SaasUi from "@codefly-dev/saas-ui";
import * as CodeflyLayout from "@codefly-dev/ui/layout";
import * as CodeflyDashboard from "@codefly-dev/ui/dashboard";
import * as CodeflyChat from "@codefly-dev/ui/chat";
import * as CodeflySkin from "@codefly-dev/ui/skin";
import * as CodeflyTable from "@codefly-dev/ui/table";
import * as CodeflyPluginHost from "@codefly-dev/ui/plugin-host";
import * as CodeflyPluginRuntime from "@codefly-dev/ui/plugin-host/runtime";
import * as CodeflyPluginUi from "@codefly-dev/ui/plugin-host/ui";
import * as CodeflyUi from "@codefly-dev/ui";
import {
	createInstance,
	type ModuleFederation,
} from "@module-federation/runtime";
import * as React from "react";
import {
	Component,
	type ComponentType,
	lazy,
	type ReactNode,
	Suspense,
} from "react";
import * as ReactJSXRuntime from "react/jsx-runtime";
import * as ReactDOM from "react-dom";

import type { DashboardAuthoring } from "@/features/dashboard";
import { authedFetch, getToken, refreshToken } from "@/lib/connect/token-store";
import { CODEFLY_KIT_VERSION, CODEFLY_SAAS_SDK_VERSION } from "./host-runtime";

// Sealed layers. A higher layer COMPOSES what a lower layer ships but cannot
// shadow or replace it: a solution remote renders against the one true instance
// the owning layer publishes, never its own copy that the kit or another module
// would then pick up. Layers are sealed downward. (Kit invariant "Sealed
// downward" — packages/codefly-ui/ARCHITECTURE.md.)
//
// This host config is one half of that seal: every sealed layer package — React,
// the co-versioned kit, and each module UI package — is shared as a
// Module-Federation `singleton`. singleton keeps exactly ONE instance across the
// host and every remote (drop it and two copies coexist, splitting React context
// and the skin). The seal is cooperative — it also relies on each remote's build
// sharing these same packages as singletons, so the remote consumes the scope's
// instance rather than bundling and registering a competing one. `requiredVersion:
// false` records that this host imposes no version floor on the shared instance
// (versioning — which version wins — is governed separately; see the kit README).
// The `kit-shared-version` test asserts the singleton flag on this object.
const SEALED_SHARE_CONFIG = {
	singleton: true,
	requiredVersion: false,
} as const;

// The kit and SDK versions this host publishes live in host-runtime.ts, where
// the register route reads them too: registration REFUSES a remote whose
// declared requirements they do not satisfy, so both sides have to agree on one
// set of numbers. Re-exported here because the share config below and the
// `kit-shared-version` test have always read them from this module.
export { CODEFLY_KIT_VERSION, CODEFLY_SAAS_SDK_VERSION };

// The co-versioned kit + module-UI packages, sealed into the Module-Federation
// scope. This is the single source of truth the `kit-shared-version` test
// asserts against directly, so a dropped `singleton` flag fails CI.
export const CODEFLY_KIT_SHARED = {
	"@codefly-dev/ui/layout": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyLayout,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui/dashboard": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyDashboard,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui/chat": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyChat,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui/skin": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflySkin,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui/table": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyTable,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui/plugin-host": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyPluginHost,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui/plugin-host/runtime": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyPluginRuntime,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui/plugin-host/ui": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyPluginUi,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/ui": {
		version: CODEFLY_KIT_VERSION,
		lib: () => CodeflyUi,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/saas-ui": {
		version: CODEFLY_KIT_VERSION,
		lib: () => SaasUi,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"@codefly-dev/saas-sdk": {
		version: CODEFLY_SAAS_SDK_VERSION,
		lib: () => SaasSdk,
		shareConfig: SEALED_SHARE_CONFIG,
	},
} as const;

// React is the bottom of the cake and is sealed the same way: the host owns the
// single instance and every remote consumes it, so hooks and context hold across
// the boundary. `requiredVersion: false` (rather than a version range) only means
// this host asserts no version floor on the instance it publishes — it does not
// affect which instance wins (a singleton always resolves to the host's loaded
// copy); it keeps React consistent with every other sealed package.
const REACT_SHARED = {
	react: {
		version: React.version,
		lib: () => React,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"react-dom": {
		version: React.version,
		lib: () => ReactDOM,
		shareConfig: SEALED_SHARE_CONFIG,
	},
	"react/jsx-runtime": {
		version: React.version,
		lib: () => ReactJSXRuntime,
		shareConfig: SEALED_SHARE_CONFIG,
	},
} as const;

// The full sealed set the host publishes: React + kit + module UI, every one a
// singleton. The `sealed-layers` test iterates this so a package added without
// the singleton flag fails CI.
export const SEALED_SHARED = {
	...REACT_SHARED,
	...CODEFLY_KIT_SHARED,
} as const;

/**
 * Generic Module Federation host runtime.
 *
 * The host owns React and publishes it into the shared scope as a singleton, so
 * a runtime-loaded solution remote consumes the exact same React instance
 * instead of bundling its own (which would break hooks/context across the
 * boundary). The remote's build marks react/react-dom/jsx-runtime as shared
 * singletons and therefore ships without them.
 *
 * The Codefly frontend kit (`@codefly-dev/ui`, `@codefly-dev/saas-ui`,
 * `@codefly-dev/saas-sdk`) is shared the same way, so a remote imports
 * `<DatasourcesPanel gateway={…}>` and renders it against the host's one copy —
 * no bundling, and one React instance across the boundary.
 */
let host: ModuleFederation | null = null;

function hostInstance(): ModuleFederation {
	if (host) {
		return host;
	}
	host = createInstance({
		name: "saas_host",
		remotes: [],
		shared: SEALED_SHARED,
	});
	return host;
}

// Tracks the entry URL each remote name is currently registered with, so a
// solution that redeploys under a new manifestUrl (same id) re-registers with
// the new entry instead of being pinned to the stale one for the life of the
// process.
const registeredEntries = new Map<string, string>();
const remoteComponents = new Map<string, ComponentType<SolutionPageProps>>();

// A cold-start race shows up as a FETCH failure: the manifest / remoteEntry
// request loses to a still-warming backend and Module Federation throws
// "Failed to fetch" / "Failed to get manifest ...". Those are worth retrying —
// the very next fetch usually wins. Everything else is deterministic and will
// fail identically on every attempt: the remote loaded but exposed no default,
// or its module threw while evaluating (a real bug in the remote), or the
// manifest 404s because the URL is wrong. Retrying those just pins the user on
// the loading spinner for the full backoff budget (~5s of setTimeouts) and
// hammers registerRemotes, only to surface the same error. Retry ONLY the
// transient fetch failures; surface deterministic errors immediately.
function isTransientRemoteLoadError(err: unknown): boolean {
	const message =
		err instanceof Error ? err.message : typeof err === "string" ? err : "";
	// Match the network/manifest-fetch failure signatures MF surfaces on a lost
	// cold-start race. A remote that responds with a 404/500 for the manifest is
	// still a fetch-layer failure that a retry can win once the backend is warm.
	return /failed to fetch|failed to get manifest|failed to get remote|networkerror|load failed|err_|fetch failed/i.test(
		message,
	);
}

/**
 * Resolve the lazy component for a remote. Declared at module scope (not in
 * render) so each remote's component is created once and stays stable across
 * renders — required by react-hooks/static-components and needed for Suspense
 * to keep its state.
 */
function remoteComponent(
	remote: SolutionRemote,
): ComponentType<SolutionPageProps> {
	const key = `${remote.id}|${remote.manifestUrl}|${remote.exposedModule}`;
	const cached = remoteComponents.get(key);
	if (cached) {
		return cached;
	}
	const federation = hostInstance();
	if (registeredEntries.get(remote.id) !== remote.manifestUrl) {
		// force:true so a changed entry URL for an already-registered name
		// replaces the stale entry rather than being ignored.
		federation.registerRemotes(
			[{ name: remote.id, entry: remote.manifestUrl }],
			{ force: true },
		);
		registeredEntries.set(remote.id, remote.manifestUrl);
	}
	const moduleKey = `${remote.id}/${remote.exposedModule.replace(/^\.\//, "")}`;
	const component = lazy(async () => {
		// The manifest fetch can lose a cold-start race (the remote's backend and
		// this dev route both warming up), throwing "Failed to get manifest /
		// Failed to fetch". React.lazy caches the first rejection for the life of
		// the component, so a single transient miss pins the solution to its error
		// boundary even though the very next fetch succeeds. Retry ONLY those
		// transient fetch failures, with backoff, re-registering the entry each
		// time so Module Federation drops its cached failed snapshot and re-fetches.
		// A deterministic failure (no default, remote threw, bad URL) breaks out
		// immediately — see isTransientRemoteLoadError. On final failure the cached
		// lazy is evicted so a remount can retry rather than being pinned to it.
		const maxAttempts = 6;
		let lastErr: unknown;
		for (let attempt = 0; attempt < maxAttempts; attempt++) {
			try {
				const mod = await federation.loadRemote<{
					default: ComponentType<SolutionPageProps>;
				}>(moduleKey);
				if (!mod?.default) {
					throw new Error(`solution remote "${remote.id}" exposed no default`);
				}
				return { default: mod.default };
			} catch (err) {
				lastErr = err;
				// A deterministic failure (no default, remote module threw, bad URL)
				// will repeat on every attempt. Don't burn the retry budget on it —
				// evict the cached lazy so a later remount re-attempts from scratch,
				// then surface it now.
				if (!isTransientRemoteLoadError(err)) {
					break;
				}
				if (attempt === maxAttempts - 1) {
					break;
				}
				await new Promise((resolve) =>
					setTimeout(resolve, 250 * (attempt + 1)),
				);
				federation.registerRemotes(
					[{ name: remote.id, entry: remote.manifestUrl }],
					{ force: true },
				);
			}
		}
		// React.lazy caches this factory's rejection for the life of the component
		// instance, so the cached lazy is now permanently failed. Evict it so a
		// remount (the user navigating back, or the error boundary being reset)
		// builds a fresh lazy and retries, instead of being pinned to this failure
		// for the life of the process even after the backend is healthy.
		remoteComponents.delete(key);
		throw lastErr;
	});
	remoteComponents.set(key, component);
	return component;
}

export interface SolutionRemote {
	id: string;
	/** MF manifest (`mf-manifest.json`) or `remoteEntry.js` URL. */
	manifestUrl: string;
	/** Exposed module key, e.g. `./Page`. */
	exposedModule: string;
}

/** Props the host injects into every solution page. */
export interface SolutionPageProps {
	solutionId: string;
	/** Same-origin base the remote must use for all backend calls (the gateway BFF). */
	apiBase: string;
	/** Host-owned access-token getter — the remote never touches the token store. */
	getAccessToken: () => string | null;
	/**
	 * Host-owned refresh: exchanges the httpOnly session for a fresh access token
	 * (single-flight) and resolves to it, or null if the session is gone. The
	 * remote hands this to `<DatasourcesPanel gateway>` so a data call that hits
	 * the token's expiry mid-session recovers instead of failing — the same
	 * mid-session recovery the portal's own transport does.
	 */
	refreshAccessToken: () => Promise<string | null>;
	/**
	 * Host-owned authed fetch: stamps the bearer token, and on a 401 exchanges
	 * the session for a fresh token (single-flight) and retries the request once.
	 * If the session is truly gone the host has already redirected to login. A
	 * solution making raw REST calls uses this instead of hand-rolling
	 * `fetch(..., { Authorization: Bearer getAccessToken() })`, so every solution
	 * gets the portal's refresh-then-retry recovery — and the dead-session
	 * auto-relogin — for free, rather than surfacing a bare `HTTP 401`.
	 */
	authedFetch: (
		input: RequestInfo | URL,
		init?: RequestInit,
	) => Promise<Response>;
	/**
	 * The host's dashboard-authoring capability, injected into the mounted
	 * runtime so a composing module can change the live dashboard: list the
	 * event vocabulary, preview a metric against the viewer's audit data, and
	 * commit a spec through validation into the host's draft. The module calls
	 * this handle; it never learns how the host validates, persists, scopes, or
	 * renders the result. A rejected spec comes back as a structured error the
	 * caller can correct, not a throw — the seam that lets an external driver own
	 * "what to change" while the host keeps "how to apply it".
	 */
	dashboardAuthoring: DashboardAuthoring;
}

/**
 * Contains a failed remote load to this outlet. A runtime remote is
 * independently deployed and can 404, time out, expose no default, or throw on
 * mount — none of which the host controls. Without this boundary that rejection
 * escapes Suspense to the nearest ancestor boundary and takes down the whole
 * dashboard route; here it degrades to a localized "failed to load" panel.
 */
class SolutionErrorBoundary extends Component<
	{ children: ReactNode },
	{ failed: boolean }
> {
	state = { failed: false };

	static getDerivedStateFromError(): { failed: boolean } {
		return { failed: true };
	}

	render() {
		if (this.state.failed) {
			return (
				<div className="p-6 text-sm opacity-70">
					This solution failed to load.
				</div>
			);
		}
		return this.props.children;
	}
}

export function SolutionOutlet({
	remote,
	pageProps,
	authoring,
}: {
	remote: SolutionRemote;
	// The server route supplies everything except the client-only capabilities:
	// the token getters and the dashboard-authoring handle, all injected here so
	// the remote never touches the token store or constructs its own handle.
	pageProps: Omit<
		SolutionPageProps,
		| "getAccessToken"
		| "refreshAccessToken"
		| "authedFetch"
		| "dashboardAuthoring"
	>;
	authoring: DashboardAuthoring;
}) {
	const Remote = remoteComponent(remote);

	return (
		<SolutionErrorBoundary key={remote.id}>
			<Suspense
				fallback={
					<div className="p-6 text-sm opacity-70">Loading solution…</div>
				}
			>
				{/* eslint-disable-next-line react-hooks/static-components -- a solution's ./Page is a Module Federation remote loaded at runtime; it cannot be a static component. It is cached at module scope (remoteComponent) so it stays stable across renders. */}
				<Remote
					{...pageProps}
					getAccessToken={getToken}
					refreshAccessToken={refreshToken}
					authedFetch={authedFetch}
					dashboardAuthoring={authoring}
				/>
			</Suspense>
		</SolutionErrorBoundary>
	);
}
