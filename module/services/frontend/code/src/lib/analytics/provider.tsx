"use client";

import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useEffect,
	useRef,
} from "react";
import { useAuth } from "@/lib/auth";
import { usePublicRuntimeConfig } from "@/lib/public-runtime-config-provider";
import type { BrowserEventName } from "./browser";
import {
	createBrowserAnalytics,
	type BrowserAnalyticsRuntime,
} from "./runtime";

type AnalyticsContextValue = {
	track: (
		eventName: BrowserEventName,
		properties?: Record<string, string | number | boolean | null>,
	) => Promise<boolean>;
};

const AnalyticsContext = createContext<AnalyticsContextValue | null>(null);

export function AnalyticsProvider({ children }: { children: ReactNode }) {
	const analytics = useRef<BrowserAnalyticsRuntime | null>(null);
	const { isAuthenticated, isLoading, organizationId, user } = useAuth();
	// The deployment's product-analytics group, read per request by the root
	// layout — never the build, which for a deployed image saw nothing.
	const { mode, host, apiKey } = usePublicRuntimeConfig().productAnalytics;

	useEffect(() => {
		analytics.current = createBrowserAnalytics({
			mode,
			host,
			apiKey,
			storage: window.localStorage,
			route: window.location.pathname,
			release: process.env.NEXT_PUBLIC_RELEASE,
			environment: process.env.NEXT_PUBLIC_ENVIRONMENT,
		});
		return () => {
			analytics.current?.reset();
			analytics.current = null;
		};
	}, [mode, host, apiKey]);

	useEffect(() => {
		if (isLoading || !analytics.current) return;
		if (isAuthenticated && user?.id) {
			void analytics.current.identify(user.id, organizationId).catch(() => {});
		} else {
			analytics.current.reset();
		}
	}, [isAuthenticated, isLoading, organizationId, user?.id]);

	useEffect(() => {
		const updateConsent = (event: Event) => {
			const detail = (event as CustomEvent).detail as
				| { analytics?: boolean; marketing?: boolean }
				| undefined;
			analytics.current?.setConsent(
				"product",
				detail?.analytics ? "granted" : "denied",
			);
			analytics.current?.setConsent(
				"marketing",
				detail?.marketing ? "granted" : "denied",
			);
			if (detail?.analytics && isAuthenticated && user?.id) {
				void analytics.current
					?.identify(user.id, organizationId)
					.catch(() => {});
			}
		};
		window.addEventListener("consentchange", updateConsent);
		return () => window.removeEventListener("consentchange", updateConsent);
	}, [isAuthenticated, organizationId, user?.id]);

	const track = useCallback<AnalyticsContextValue["track"]>(
		async (eventName, properties = {}) =>
			analytics.current?.track(eventName, properties) ?? false,
		[],
	);

	return (
		<AnalyticsContext.Provider value={{ track }}>
			{children}
		</AnalyticsContext.Provider>
	);
}

export function useAnalytics(): AnalyticsContextValue {
	const value = useContext(AnalyticsContext);
	if (!value) throw new Error("useAnalytics must be used within AnalyticsProvider");
	return value;
}
