"use client";

import type { FrontendThemePreference } from "@codefly/saas-plugin-contract";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useEffect,
	useRef,
} from "react";
import { useAuth } from "@/lib/auth";
import { useTheme } from "@/lib/theme-provider";
import { Settings } from "../model/settings";
import {
	isThemePreference,
	themePreferenceFromProto,
	themePreferenceToProto,
} from "../model/theme-preference";
import { userSettingsMutations } from "../service/mutations";
import { userSettingsQueries } from "../service/queries";

interface ThemePreferenceContextValue {
	preference: FrontendThemePreference;
	isSaving: boolean;
	setPreference: (preference: FrontendThemePreference) => Promise<void>;
}

const ThemePreferenceContext =
	createContext<ThemePreferenceContextValue | null>(null);

/**
 * Owns both browser and account persistence. The server preference is applied
 * once per authenticated user/value; subsequent local selections are never
 * fought by a synchronization effect while their mutation is in flight.
 */
export function ThemePreferenceProvider({ children }: { children: ReactNode }) {
	const queryClient = useQueryClient();
	const { user, isAuthenticated, isLoading: authLoading } = useAuth();
	const { theme, setTheme } = useTheme();
	const appliedServerValue = useRef("");
	const { data: settings } = useQuery({
		...userSettingsQueries.current(),
		enabled: isAuthenticated && !authLoading,
	});
	const mutation = useMutation({ mutationFn: userSettingsMutations.update });

	useEffect(() => {
		if (!isAuthenticated || !user?.id) {
			appliedServerValue.current = "";
			return;
		}
		if (!settings) return;
		const serverTheme = Settings.appearance.theme.get(settings);
		const serverPreference = themePreferenceFromProto(serverTheme);
		if (!serverPreference) return;
		const identity = `${user.id}:${serverPreference}`;
		if (appliedServerValue.current === identity) return;
		appliedServerValue.current = identity;
		setTheme(serverPreference);
	}, [isAuthenticated, settings, setTheme, user?.id]);

	const setPreference = useCallback(
		async (preference: FrontendThemePreference) => {
			const previous = isThemePreference(theme) ? theme : "system";
			setTheme(preference);
			if (!isAuthenticated) return;
			try {
				const updated = await mutation.mutateAsync({
					patch: Settings.appearance.theme.patch(
						themePreferenceToProto(preference),
					),
				});
				queryClient.setQueryData(["user-settings"], updated);
			} catch (error) {
				setTheme(previous);
				throw error;
			}
		},
		[isAuthenticated, mutation, queryClient, setTheme, theme],
	);

	const preference = isThemePreference(theme) ? theme : "system";
	return (
		<ThemePreferenceContext.Provider
			value={{ preference, isSaving: mutation.isPending, setPreference }}
		>
			{children}
		</ThemePreferenceContext.Provider>
	);
}

export function useThemePreference(): ThemePreferenceContextValue {
	const context = useContext(ThemePreferenceContext);
	if (!context)
		throw new Error(
			"useThemePreference must be used within ThemePreferenceProvider",
		);
	return context;
}
