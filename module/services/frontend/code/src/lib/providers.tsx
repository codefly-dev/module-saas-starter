"use client";

import type { FrontendReactConfig } from "@codefly-dev/ui/plugin-host";
import { PluginRuntimeProvider } from "@codefly-dev/ui/plugin-host/runtime";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useRef,
	useState,
} from "react";
import { ThemePreferenceProvider } from "@/features/user-settings/ui/theme-preference-provider";
import { AnalyticsProvider } from "@/lib/analytics/provider";
import { AppearanceProvider } from "@/lib/appearance-provider";
import {
	type PublicRuntimeConfig,
	UNCONFIGURED_PUBLIC_RUNTIME_CONFIG,
} from "@/lib/public-runtime-config";
import { PublicRuntimeConfigProvider } from "@/lib/public-runtime-config-provider";
import { ThemeProvider } from "@/lib/theme-provider";
import applicationFrontendConfig from "../../frontend.config";
import { AuthProvider, useAuth } from "./auth";
import { hostPluginRuntime } from "./plugins/runtime";

const FrontendConfigContext = createContext<FrontendReactConfig | null>(null);

export function useFrontendConfig(): FrontendReactConfig {
	const config = useContext(FrontendConfigContext);
	if (!config)
		throw new Error(
			"useFrontendConfig must be used within FrontendConfigProvider",
		);
	return config;
}

export function FrontendConfigProvider({
	config,
	children,
}: {
	config: FrontendReactConfig;
	children: ReactNode;
}) {
	return (
		<FrontendConfigContext.Provider value={config}>
			{children}
		</FrontendConfigContext.Provider>
	);
}

// A different authority must never reuse queries or local component state from
// the previous session. Changing this key replaces the entire authenticated
// subtree atomically, before its children can read a previous authority's cache.
//
// The key is the session's authority as the provider states it (`authority`),
// which does not change when the host confirms the session it already is. Built
// here from the roles the context shows, it changed on every page load as that
// confirmation arrived, and each load made the shell's reads twice.
function AuthorityBoundary({ children }: { children: ReactNode }) {
	const { authority } = useAuth();
	return <AuthorityQueries key={authority}>{children}</AuthorityQueries>;
}

function AuthorityQueries({ children }: { children: ReactNode }) {
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
	// The cache goes with the authority it was read under. It is dropped once
	// this boundary is really gone, not on the unmount React's development build
	// rehearses as it mounts (StrictMode): dropping it there discarded reads
	// already in flight, and every query of the shell was made twice.
	const mounted = useRef(false);
	useEffect(() => {
		mounted.current = true;
		return () => {
			mounted.current = false;
			queueMicrotask(() => {
				if (!mounted.current) queryClient.clear();
			});
		};
	}, [queryClient]);
	return (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

export function Providers({
	children,
	frontendConfig = applicationFrontendConfig,
	publicRuntimeConfig = UNCONFIGURED_PUBLIC_RUNTIME_CONFIG,
}: {
	children: ReactNode;
	frontendConfig?: FrontendReactConfig;
	/** The deployment's browser-safe configuration, read per request. */
	publicRuntimeConfig?: PublicRuntimeConfig;
}) {
	// ThemeProvider wraps everything below the configuration so cmd+K, toasts,
	// and the admin chrome all read the same theme. attribute="class" behavior
	// matches the .dark CSS variant in globals.css. defaultTheme "system"
	// honours the OS preference until the user picks one.
	return (
		<PublicRuntimeConfigProvider config={publicRuntimeConfig}>
			<ThemeProvider
				defaultTheme={frontendConfig.appearance.defaultTheme}
				enableSystem
				disableTransitionOnChange
			>
				<AuthProvider>
					<AuthorityBoundary>
						<AnalyticsProvider>
							<FrontendConfigProvider config={frontendConfig}>
								<AppearanceProvider config={frontendConfig}>
									<ThemePreferenceProvider>
										<PluginRuntimeProvider runtime={hostPluginRuntime}>
											{children}
										</PluginRuntimeProvider>
									</ThemePreferenceProvider>
								</AppearanceProvider>
							</FrontendConfigProvider>
						</AnalyticsProvider>
					</AuthorityBoundary>
				</AuthProvider>
			</ThemeProvider>
		</PublicRuntimeConfigProvider>
	);
}
