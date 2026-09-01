import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import {
	defineReactFrontend,
	defineReactPlugin,
} from "@codefly-dev/ui/plugin-host";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppearanceProvider } from "@/lib/appearance-provider";
import { FrontendConfigProvider } from "@/lib/providers";

vi.mock("next/navigation", () => ({
	usePathname: () => "/admin/injected",
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
vi.mock("@/lib/hooks/use-users-search", () => ({ useUsersSearch: () => [] }));
vi.mock("@/features/notifications/ui/notification-bell", () => ({
	NotificationBell: () => null,
}));
vi.mock("@/components/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@/components/rate-limit-banner", () => ({
	RateLimitBanner: () => null,
}));

import PluginRoutePage from "@/app/admin/[...slug]/page";
import AdminOverviewPage from "@/app/admin/page";
import { AdminLayout } from "@/components/admin-layout";
import { CommandPalette } from "@/components/command-palette";
import { Slot } from "@/components/slot";

const InjectedRoute = lazy(async () => ({
	default: () => <div>Injected route</div>,
}));
const InjectedWidget = lazy(async () => ({
	default: () => <div>Injected widget</div>,
}));

const alternateConfig = defineReactFrontend({
	branding: {
		name: "Injected Application",
		mark: "I",
		title: "Injected title",
		description: "Injected description",
	},
	plugins: [
		defineReactPlugin({
			manifest: definePlugin({
				contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
				name: "injected",
				navigation: { label: "Injected", placement: "admin" },
				navItems: [
					{
						label: "Injected navigation",
						href: "/admin/injected",
						icon: "Shield",
						group: "Injected group",
						requiredRole: "admin",
						surfaces: ["sidebar", "command_palette", "plugin_registry"],
					},
				],
				routes: [
					{
						id: "overview",
						path: "/admin/injected",
						requiredRole: "admin",
					},
				],
				widgets: [
					{
						id: "injected.widget",
						requiredRole: "admin",
					},
				],
			}),
			routes: [{ id: "overview", component: InjectedRoute }],
			widgets: [{ id: "injected.widget", component: InjectedWidget }],
		}),
	],
});

function Injected({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return (
		<FrontendConfigProvider config={alternateConfig}>
			<QueryClientProvider client={queryClient}>
				<AppearanceProvider config={alternateConfig}>
					{children}
				</AppearanceProvider>
			</QueryClientProvider>
		</FrontendConfigProvider>
	);
}

afterEach(cleanup);

describe("injected frontend config adapters", () => {
	it("drives the canonical shell and branding", () => {
		render(
			<Injected>
				<AdminLayout>content</AdminLayout>
			</Injected>,
		);
		expect(screen.getByText("Injected Application")).toBeTruthy();
		expect(screen.getByText("Injected")).toBeTruthy();
		expect(screen.getByText("Injected navigation")).toBeTruthy();
	});

	it("drives the named widget slot", async () => {
		render(
			<Injected>
				<Slot name="dashboard.widgets" />
			</Injected>,
		);
		expect(await screen.findByText("Injected widget")).toBeTruthy();
	});

	it("drives the plugin route outlet", async () => {
		render(
			<Injected>
				<PluginRoutePage />
			</Injected>,
		);
		expect(await screen.findByText("Injected route")).toBeTruthy();
	});

	it("drives the admin landing tiles", () => {
		render(
			<Injected>
				<AdminOverviewPage />
			</Injected>,
		);
		expect(screen.getByText("Injected navigation")).toBeTruthy();
	});

	it("drives command discovery", async () => {
		render(
			<Injected>
				<CommandPalette />
			</Injected>,
		);
		fireEvent.keyDown(window, { key: "k", metaKey: true });
		expect(await screen.findByText("Injected navigation")).toBeTruthy();
	});
});
