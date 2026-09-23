"use client";

import { createContext, type ReactNode, useContext } from "react";

import {
	type PublicRuntimeConfig,
	UNCONFIGURED_PUBLIC_RUNTIME_CONFIG,
} from "./public-runtime-config";

const PublicRuntimeConfigContext = createContext<PublicRuntimeConfig>(
	UNCONFIGURED_PUBLIC_RUNTIME_CONFIG,
);

/**
 * Carries the configuration the root layout read for this request down to the
 * client components that need it. Without a provider a component sees an
 * unconfigured deployment — every optional surface off — which is what it saw
 * before, when the value came from an empty build.
 */
export function PublicRuntimeConfigProvider({
	config,
	children,
}: {
	config: PublicRuntimeConfig;
	children: ReactNode;
}) {
	return (
		<PublicRuntimeConfigContext.Provider value={config}>
			{children}
		</PublicRuntimeConfigContext.Provider>
	);
}

export function usePublicRuntimeConfig(): PublicRuntimeConfig {
	return useContext(PublicRuntimeConfigContext);
}
