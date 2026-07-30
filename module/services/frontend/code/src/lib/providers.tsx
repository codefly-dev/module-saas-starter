"use client";

import type { FrontendReactConfig } from "@codefly/saas-plugin-react";
import { PluginRuntimeProvider } from "@codefly/saas-plugin-react/runtime";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createContext, type ReactNode, useContext, useState } from "react";
import { ThemePreferenceProvider } from "@/features/user-settings/ui/theme-preference-provider";
import { AppearanceProvider } from "@/lib/appearance-provider";
import { AnalyticsProvider } from "@/lib/analytics/provider";
import { ThemeProvider } from "@/lib/theme-provider";
import applicationFrontendConfig from "../../frontend.config";
import { AuthProvider } from "./auth";
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

export function Providers({
	children,
	frontendConfig = applicationFrontendConfig,
}: {
	children: ReactNode;
	frontendConfig?: FrontendReactConfig;
}) {
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
			<QueryClientProvider client={queryClient}>
				<AuthProvider>
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
				</AuthProvider>
			</QueryClientProvider>
		</ThemeProvider>
	);
}
