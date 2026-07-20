"use client";

import type {
	FrontendBranding,
	FrontendConfig,
} from "@codefly/saas-plugin-contract";
import { useQuery } from "@tanstack/react-query";
import {
	createContext,
	type ReactNode,
	useContext,
	useEffect,
	useMemo,
} from "react";
import { orgQueries } from "@/features/organizations/service/queries";
import { appearanceVariableName, readableForeground } from "./appearance";
import { useAuth } from "./auth";

interface AppearanceContextValue {
	branding: FrontendBranding;
	isTenantBranded: boolean;
}

const AppearanceContext = createContext<AppearanceContextValue | null>(null);
const HEX_COLOR = /^#[0-9a-f]{6}$/i;

function safeBrandAsset(value: string | undefined): string | undefined {
	if (!value) return undefined;
	if (value.startsWith("/") && !value.startsWith("//")) return value;
	try {
		const parsed = new URL(value);
		return parsed.protocol === "https:" ? parsed.toString() : undefined;
	} catch {
		return undefined;
	}
}

/**
 * Applies the explicitly supported tenant overlay. A tenant may replace its
 * logo/favicon and primary semantic color; it cannot inject arbitrary CSS,
 * alter layout tokens, or replace application-owned plugin composition.
 */
export function AppearanceProvider({
	config,
	children,
}: {
	config: FrontendConfig;
	children: ReactNode;
}) {
	const { organizationId, isAuthenticated, isLoading } = useAuth();
	const { data: organizationAppearance } = useQuery({
		...orgQueries.settings(organizationId ?? ""),
		enabled: isAuthenticated && !isLoading && Boolean(organizationId),
	});
	const primaryColor = HEX_COLOR.test(
		organizationAppearance?.primaryColor ?? "",
	)
		? organizationAppearance?.primaryColor
		: undefined;
	const tenantLogo = safeBrandAsset(organizationAppearance?.logoUrl);
	const tenantFavicon = safeBrandAsset(organizationAppearance?.faviconUrl);

	useEffect(() => {
		if (!primaryColor) return;
		const root = document.documentElement;
		const foreground = readableForeground(primaryColor);
		const overrides = [
			["primary", primaryColor],
			["primaryForeground", foreground],
			["ring", primaryColor],
			["sidebarPrimary", primaryColor],
			["sidebarPrimaryForeground", foreground],
			["chart1", primaryColor],
		] as const;
		const previous = new Map<string, string>();
		for (const mode of ["light", "dark"] as const) {
			for (const [token, value] of overrides) {
				const property = appearanceVariableName(mode, token);
				previous.set(property, root.style.getPropertyValue(property));
				root.style.setProperty(property, value);
			}
		}
		return () => {
			for (const [property, value] of previous) {
				if (value) root.style.setProperty(property, value);
				else root.style.removeProperty(property);
			}
		};
	}, [primaryColor]);

	useEffect(() => {
		if (!tenantFavicon) return;
		const existing =
			document.querySelector<HTMLLinkElement>('link[rel~="icon"]');
		const link = existing ?? document.createElement("link");
		const previousHref = existing?.href;
		if (!existing) {
			link.rel = "icon";
			document.head.appendChild(link);
		}
		link.href = tenantFavicon;
		return () => {
			if (!existing) link.remove();
			else if (previousHref) existing.href = previousHref;
		};
	}, [tenantFavicon]);

	const branding = useMemo<FrontendBranding>(() => {
		if (!tenantLogo) return config.branding;
		return {
			...config.branding,
			logo: {
				lightSrc: tenantLogo,
				darkSrc: tenantLogo,
				alt: `${config.branding.name} organization logo`,
			},
		};
	}, [config.branding, tenantLogo]);

	return (
		<AppearanceContext.Provider
			value={{
				branding,
				isTenantBranded: Boolean(primaryColor || tenantLogo || tenantFavicon),
			}}
		>
			{children}
		</AppearanceContext.Provider>
	);
}

export function useAppearance(): AppearanceContextValue {
	const context = useContext(AppearanceContext);
	if (!context)
		throw new Error("useAppearance must be used within AppearanceProvider");
	return context;
}
