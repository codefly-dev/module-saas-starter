"use client";

import {
	createInstance,
	type ModuleFederation,
} from "@module-federation/runtime";
import * as React from "react";
import * as ReactDOM from "react-dom";
import * as ReactJSXRuntime from "react/jsx-runtime";
import { Suspense, lazy, useMemo, type ComponentType } from "react";

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

const registered = new Set<string>();

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
}

export function SolutionOutlet({
	remote,
	pageProps,
}: {
	remote: SolutionRemote;
	// The server route supplies everything except the client-only token getter,
	// which the host injects here so the remote never touches the token store.
	pageProps: Omit<SolutionPageProps, "getAccessToken">;
}) {
	const Remote = useMemo(() => {
		const federation = hostInstance();
		if (!registered.has(remote.id)) {
			federation.registerRemotes([
				{ name: remote.id, entry: remote.manifestUrl },
			]);
			registered.add(remote.id);
		}
		const moduleKey = `${remote.id}/${remote.exposedModule.replace(/^\.\//, "")}`;
		return lazy(async () => {
			const mod = await federation.loadRemote<{
				default: ComponentType<SolutionPageProps>;
			}>(moduleKey);
			if (!mod?.default) {
				throw new Error(`solution remote "${remote.id}" exposed no default`);
			}
			return { default: mod.default };
		});
	}, [remote.id, remote.manifestUrl, remote.exposedModule]);

	return (
		<Suspense fallback={<div className="p-6 text-sm opacity-70">Loading solution…</div>}>
			<Remote {...pageProps} getAccessToken={getToken} />
		</Suspense>
	);
}
