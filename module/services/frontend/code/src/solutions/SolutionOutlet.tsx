"use client";

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
import { getToken } from "@/lib/connect/token-store";

/**
 * Generic Module Federation host runtime.
 *
 * The host owns React and publishes it into the shared scope as a singleton, so
 * a runtime-loaded solution remote consumes the exact same React instance
 * instead of bundling its own (which would break hooks/context across the
 * boundary). The remote's build marks react/react-dom/jsx-runtime as shared
 * singletons and therefore ships without them.
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
	// the token getter and the dashboard-authoring handle, both injected here so
	// the remote never touches the token store or constructs its own handle.
	pageProps: Omit<SolutionPageProps, "getAccessToken" | "dashboardAuthoring">;
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
					dashboardAuthoring={authoring}
				/>
			</Suspense>
		</SolutionErrorBoundary>
	);
}
