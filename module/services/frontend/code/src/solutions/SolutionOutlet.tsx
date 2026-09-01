"use client";

import * as SaasSdk from "@codefly/saas-sdk";
import * as SaasUi from "@codefly/saas-ui";
import * as CodeflyUi from "@codefly-dev/ui";
import {
	createInstance,
	type ModuleFederation,
} from "@module-federation/runtime";
import * as React from "react";
import * as ReactDOM from "react-dom";
import * as ReactJSXRuntime from "react/jsx-runtime";
import {
	Component,
	Suspense,
	lazy,
	type ComponentType,
	type ReactNode,
} from "react";

import type { DashboardAuthoring } from "@/features/dashboard";
import { authedFetch, getToken, refreshToken } from "@/lib/connect/token-store";

// The co-versioned @codefly/* kit ships lockstep with this host, so one version
// covers all three. It MUST track the packages' real version — a shared module
// that under-reports its version can lose singleton resolution to a remote that
// bundles a higher one, splitting the instance the dedup exists to keep single.
// The `kit-shared-version` test pins this to the packages' actual versions so a
// bump can't drift it silently. requiredVersion is left off the share config:
// the host publishes this exact instance, and a remote resolves to it without
// version negotiation.
export const CODEFLY_KIT_VERSION = "0.1.0";

/**
 * Generic Module Federation host runtime.
 *
 * The host owns React and publishes it into the shared scope as a singleton, so
 * a runtime-loaded solution remote consumes the exact same React instance
 * instead of bundling its own (which would break hooks/context across the
 * boundary). The remote's build marks react/react-dom/jsx-runtime as shared
 * singletons and therefore ships without them.
 *
 * The Codefly frontend kit (`@codefly-dev/ui`, `@codefly/saas-ui`,
 * `@codefly/saas-sdk`) is shared the same way, so a remote imports
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
		shared: {
			react: {
				version: React.version,
				lib: () => React,
				shareConfig: { singleton: true, requiredVersion: `^${React.version}` },
			},
			"react-dom": {
				version: React.version,
				lib: () => ReactDOM,
				shareConfig: { singleton: true, requiredVersion: `^${React.version}` },
			},
			"react/jsx-runtime": {
				version: React.version,
				lib: () => ReactJSXRuntime,
				shareConfig: { singleton: true, requiredVersion: `^${React.version}` },
			},
			"@codefly-dev/ui": {
				version: CODEFLY_KIT_VERSION,
				lib: () => CodeflyUi,
				shareConfig: { singleton: true, requiredVersion: false },
			},
			"@codefly/saas-ui": {
				version: CODEFLY_KIT_VERSION,
				lib: () => SaasUi,
				shareConfig: { singleton: true, requiredVersion: false },
			},
			"@codefly/saas-sdk": {
				version: CODEFLY_KIT_VERSION,
				lib: () => SaasSdk,
				shareConfig: { singleton: true, requiredVersion: false },
			},
		},
	});
	return host;
}

// Tracks the entry URL each remote name is currently registered with, so a
// solution that redeploys under a new manifestUrl (same id) re-registers with
// the new entry instead of being pinned to the stale one for the life of the
// process.
const registeredEntries = new Map<string, string>();
const remoteComponents = new Map<string, ComponentType<SolutionPageProps>>();

/**
 * Resolve the lazy component for a remote. Declared at module scope (not in
 * render) so each remote's component is created once and stays stable across
 * renders — required by react-hooks/static-components and needed for Suspense
 * to keep its state.
 */
function remoteComponent(remote: SolutionRemote): ComponentType<SolutionPageProps> {
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
		const mod = await federation.loadRemote<{
			default: ComponentType<SolutionPageProps>;
		}>(moduleKey);
		if (!mod?.default) {
			throw new Error(`solution remote "${remote.id}" exposed no default`);
		}
		return { default: mod.default };
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
	authedFetch: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
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
		"getAccessToken" | "refreshAccessToken" | "authedFetch" | "dashboardAuthoring"
	>;
	authoring: DashboardAuthoring;
}) {
	const Remote = remoteComponent(remote);

	return (
		<SolutionErrorBoundary key={remote.id}>
			<Suspense fallback={<div className="p-6 text-sm opacity-70">Loading solution…</div>}>
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
