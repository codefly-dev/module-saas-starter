"use client";

import type { FrontendReactConfig } from "@codefly-dev/ui/plugin-host";
import { PluginRuntimeProvider } from "@codefly-dev/ui/plugin-host/runtime";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useState,
} from "react";
import { ThemePreferenceProvider } from "@/features/user-settings/ui/theme-preference-provider";
import { AnalyticsProvider } from "@/lib/analytics/provider";
import { AppearanceProvider } from "@/lib/appearance-provider";
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
function AuthorityBoundary({ children }: { children: ReactNode }) {
	const auth = useAuth();
	const authority = JSON.stringify([
		auth.isAuthenticated,
		auth.user?.id,
		auth.organizationId,
		auth.orgRole,
		auth.platformRole,
		auth.impersonation.isImpersonating,
		auth.impersonation.impersonatorId,
		auth.impersonation.subjectId,
	]);
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
	useEffect(
		() => () => {
			queryClient.clear();
		},
		[queryClient],
	);
	return (
		<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
	);
}

export function Providers({
	children,
	frontendConfig = applicationFrontendConfig,
}: {
	children: ReactNode;
	frontendConfig?: FrontendReactConfig;
}) {
	return (
		// ThemeProvider wraps everything so cmd+K, toasts, and the
		// admin chrome all read the same theme. attribute="class"
		// behavior matches the .dark CSS variant in globals.css. defaultTheme
		// "system" honours the OS preference until the user picks one.
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
	);
}
