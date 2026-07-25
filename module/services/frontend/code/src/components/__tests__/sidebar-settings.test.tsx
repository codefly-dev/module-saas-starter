import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FRONTEND_NAVIGATION } from "@/gen/saas/frontend/v1/plugin_catalog";
import { AppearanceProvider } from "@/lib/appearance-provider";
import { FrontendConfigProvider } from "@/lib/providers";

vi.mock("next/navigation", () => ({
	usePathname: () => "/",
	useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
	notFound: () => {
		throw new Error("not found");
	},
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		user: { id: "user-1", email: "admin@example.com" },
		isAuthenticated: true,
		isLoading: false,
		platformRole: "super_admin",
		orgRole: undefined,
		logout: vi.fn(),
		getToken: () => "token",
	}),
}));
vi.mock("@/features/notifications/ui/notification-bell", () => ({
	NotificationBell: () => null,
}));
vi.mock("@/components/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@/components/rate-limit-banner", () => ({
	RateLimitBanner: () => null,
}));

import { AdminLayout } from "@/components/admin-layout";
import applicationFrontendConfig from "../../../frontend.config";

function Wrap({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return (
		<FrontendConfigProvider config={applicationFrontendConfig}>
			<QueryClientProvider client={queryClient}>
				<AppearanceProvider config={applicationFrontendConfig}>
					{children}
				</AppearanceProvider>
			</QueryClientProvider>
		</FrontendConfigProvider>
	);
}

afterEach(cleanup);

describe("settings navigation placement", () => {
	it("keeps personal settings out of the main sidebar", () => {
		// Rendered as super_admin — the widest sidebar. The user-menu
		// dropdown stays closed, so any settings label appearing here would
		// mean it renders directly in the sidebar tree.
		render(
			<Wrap>
				<AdminLayout>content</AdminLayout>
			</Wrap>,
		);

		// Dashboard is a genuine sidebar item — a positive control proving
		// this probe would surface a stray sidebar entry.
		expect(screen.getByText("Dashboard")).toBeTruthy();

		expect(screen.queryByText("Security")).toBeNull();
		expect(screen.queryByText("Notifications")).toBeNull();
		expect(screen.queryByText("Data & Privacy")).toBeNull();
		expect(screen.queryByText("Settings")).toBeNull();
	});

	it("assigns the Settings group to the user menu, never the sidebar", () => {
		const settingsItems = FRONTEND_NAVIGATION.filter(
			(item) => item.group === "Settings",
		);
		expect(settingsItems.length).toBeGreaterThan(0);
		for (const item of settingsItems) {
			expect(item.surfaces).toContain("user_menu");
			expect(item.surfaces).not.toContain("sidebar");
		}
	});
});
