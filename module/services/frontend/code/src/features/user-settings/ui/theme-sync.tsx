"use client";

/**
 * ThemeSync — applies the user's persisted theme preference once
 * after login. Without this, a logged-in user on a fresh device
 * would always see the OS theme (system default) until they manually
 * cycled the toggle.
 *
 * Mounts inside the admin chrome (post-AuthProvider). Fires the
 * settings query, sets next-themes to settings.theme. No-op when
 * settings.theme is empty (user has never picked) — defaultTheme=
 * "system" already handles that.
 *
 * Renders nothing.
 */

import { useEffect } from "react";
import { useTheme } from "next-themes";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@/lib/auth";
import { userSettingsQueries } from "../service/queries";

export function ThemeSync() {
  const { isAuthenticated, isLoading: authLoading } = useAuth();
  const { setTheme, theme: currentTheme } = useTheme();
  const { data: settings } = useQuery({
    ...userSettingsQueries.current(),
    // Don't fire until auth resolves — anonymous users have no
    // settings to fetch (would 401).
    enabled: isAuthenticated && !authLoading,
  });

  useEffect(() => {
    if (!settings?.theme) return;
    if (settings.theme === currentTheme) return;
    setTheme(settings.theme);
  }, [settings?.theme, currentTheme, setTheme]);

  return null;
}
