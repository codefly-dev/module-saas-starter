"use client";

import {
	createContext,
	type ReactNode,
	useContext,
	useMemo,
	useState,
	useSyncExternalStore,
} from "react";

type ResolvedTheme = "light" | "dark";
type Theme = ResolvedTheme | "system";

interface ThemeSnapshot {
	theme: Theme;
	resolvedTheme: ResolvedTheme;
	systemTheme: ResolvedTheme;
}

interface ThemeContextValue extends ThemeSnapshot {
	setTheme: (theme: string) => void;
	themes: Theme[];
}

interface ThemeProviderProps {
	children: ReactNode;
	defaultTheme?: string;
	enableSystem?: boolean;
	disableTransitionOnChange?: boolean;
}

const STORAGE_KEY = "theme";
const FALLBACK_THEME_CONTEXT: ThemeContextValue = {
	theme: "system",
	resolvedTheme: "light",
	systemTheme: "light",
	setTheme: () => {},
	themes: ["light", "dark", "system"],
};
const ThemeContext = createContext<ThemeContextValue>(FALLBACK_THEME_CONTEXT);

function normalizeTheme(
	value: string | null | undefined,
	enableSystem: boolean,
): Theme {
	if (value === "light" || value === "dark") return value;
	return enableSystem ? "system" : "light";
}

function currentSystemTheme(): ResolvedTheme {
	return window.matchMedia("(prefers-color-scheme: dark)").matches
		? "dark"
		: "light";
}

function suppressTransitions(): () => void {
	const style = document.createElement("style");
	style.textContent =
		"*,*::before,*::after{transition:none!important;animation-duration:0s!important}";
	document.head.appendChild(style);
	return () => {
		const remove = () => style.remove();
		if (typeof window.requestAnimationFrame === "function") {
			window.requestAnimationFrame(() => window.requestAnimationFrame(remove));
			return;
		}
		window.setTimeout(remove, 0);
	};
}

class ThemeStore {
	readonly themes: Theme[];
	private readonly defaultTheme: Theme;
	private readonly serverSnapshot: ThemeSnapshot;
	private readonly listeners = new Set<() => void>();
	private snapshot: ThemeSnapshot;
	private media: MediaQueryList | null = null;
	private started = false;

	constructor(
		defaultTheme: string,
		private readonly enableSystem: boolean,
		private readonly disableTransitionOnChange: boolean,
	) {
		this.defaultTheme = normalizeTheme(defaultTheme, enableSystem);
		const resolvedTheme = this.defaultTheme === "dark" ? "dark" : "light";
		this.serverSnapshot = {
			theme: this.defaultTheme,
			resolvedTheme,
			systemTheme: "light",
		};
		this.snapshot = this.serverSnapshot;
		this.themes = enableSystem
			? ["light", "dark", "system"]
			: ["light", "dark"];
	}

	getSnapshot = (): ThemeSnapshot => this.snapshot;
	getServerSnapshot = (): ThemeSnapshot => this.serverSnapshot;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		if (!this.started) this.start();
		return () => {
			this.listeners.delete(listener);
			if (this.listeners.size === 0) this.stop();
		};
	};

	setTheme = (value: string): void => {
		const theme = normalizeTheme(value, this.enableSystem);
		try {
			window.localStorage.setItem(STORAGE_KEY, theme);
		} catch {
			// Browsers may block storage; the in-memory preference still applies.
		}
		this.update(theme, currentSystemTheme());
	};

	private start(): void {
		this.started = true;
		this.media = window.matchMedia("(prefers-color-scheme: dark)");
		let theme = this.defaultTheme;
		try {
			theme = normalizeTheme(
				window.localStorage.getItem(STORAGE_KEY) ?? this.defaultTheme,
				this.enableSystem,
			);
		} catch {
			// Use the configured default when storage is unavailable.
		}
		this.update(theme, this.media.matches ? "dark" : "light", false);
		this.media.addEventListener("change", this.onSystemThemeChange);
		window.addEventListener("storage", this.onStorage);
	}

	private stop(): void {
		this.media?.removeEventListener("change", this.onSystemThemeChange);
		window.removeEventListener("storage", this.onStorage);
		this.media = null;
		this.started = false;
	}

	private onSystemThemeChange = (event: MediaQueryListEvent): void => {
		this.update(this.snapshot.theme, event.matches ? "dark" : "light");
	};

	private onStorage = (event: StorageEvent): void => {
		if (event.key !== STORAGE_KEY) return;
		this.update(
			normalizeTheme(event.newValue ?? this.defaultTheme, this.enableSystem),
			currentSystemTheme(),
		);
	};

	private update(
		theme: Theme,
		systemTheme: ResolvedTheme,
		notify = true,
	): void {
		const resolvedTheme = theme === "system" ? systemTheme : theme;
		const changed =
			this.snapshot.theme !== theme ||
			this.snapshot.systemTheme !== systemTheme ||
			this.snapshot.resolvedTheme !== resolvedTheme;
		this.snapshot = { theme, systemTheme, resolvedTheme };
		this.apply(resolvedTheme);
		if (notify && changed) {
			for (const listener of this.listeners) listener();
		}
	}

	private apply(theme: ResolvedTheme): void {
		const restoreTransitions = this.disableTransitionOnChange
			? suppressTransitions()
			: undefined;
		document.documentElement.classList.toggle("dark", theme === "dark");
		document.documentElement.style.colorScheme = theme;
		restoreTransitions?.();
	}
}

/**
 * Script-free theme state for React 19/Next.js client rendering.
 *
 * useSyncExternalStore keeps the server snapshot stable during hydration, then
 * subscribes to localStorage and system preference changes without synchronously
 * setting React state from an Effect.
 */
export function ThemeProvider({
	children,
	defaultTheme = "system",
	enableSystem = true,
	disableTransitionOnChange = false,
}: ThemeProviderProps) {
	const [store] = useState(
		() => new ThemeStore(defaultTheme, enableSystem, disableTransitionOnChange),
	);
	const snapshot = useSyncExternalStore(
		store.subscribe,
		store.getSnapshot,
		store.getServerSnapshot,
	);
	const value = useMemo<ThemeContextValue>(
		() => ({ ...snapshot, setTheme: store.setTheme, themes: store.themes }),
		[snapshot, store],
	);

	return (
		<ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
	);
}

export function useTheme(): ThemeContextValue {
	return useContext(ThemeContext);
}
