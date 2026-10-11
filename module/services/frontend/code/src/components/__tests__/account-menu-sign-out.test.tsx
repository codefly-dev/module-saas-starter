// THE ACCOUNT MENU's "Sign out" — the primary sign-out control, on every
// dashboard and admin page — takes the shared sign-out journey.
//
// It is the control most people actually use, and it was the one left behind:
// `onClick={logout}` handed the promise to a menu item, which discards it, so an
// incomplete sign-out looked finished and the route shell then sent the person
// to the sign-in page as though the session had ended. Local state is cleared
// either way, which is what made the wrong answer invisible — the app looks
// signed out while an already-issued access token stays live until it expires.
//
// Asserted behaviourally, on where the person is SENT, because that is the whole
// difference between the two outcomes. The structural sweep beside this
// (sign-out-controls-contract.test.ts) is what finds the next control; this is
// what proves the journey runs for this one.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { AppearanceProvider } from "@/lib/appearance-provider";
import { FrontendConfigProvider } from "@/lib/providers";

const routerReplace = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({
	usePathname: () => "/",
	useRouter: () => ({ push: vi.fn(), replace: routerReplace }),
	notFound: () => {
		throw new Error("not found");
	},
}));
const logout = vi.hoisted(() => vi.fn(async () => ({ complete: false })));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		user: { id: "user-1", email: "jane@example.com", name: "Jane Doe" },
		isAuthenticated: true,
		isLoading: false,
		platformRole: undefined,
		orgRole: "admin",
		logout,
		getToken: () => "token",
	}),
}));
vi.mock("@/features/notifications/ui/notification-bell", () => ({ NotificationBell: () => null }));
vi.mock("@/features/notifications/ui/notification-banner", () => ({ NotificationBanner: () => null }));
vi.mock("@/features/organizations/ui/org-switcher", () => ({ OrgSwitcher: () => null }));
vi.mock("@/components/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@/components/rate-limit-banner", () => ({ RateLimitBanner: () => null }));

import { AdminLayout } from "@/components/admin-layout";
import applicationFrontendConfig from "../../../frontend.config";

function Wrap({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return (
		<FrontendConfigProvider config={applicationFrontendConfig}>
			<QueryClientProvider client={queryClient}>
				<AppearanceProvider config={applicationFrontendConfig}>{children}</AppearanceProvider>
			</QueryClientProvider>
		</FrontendConfigProvider>
	);
}

afterEach(() => {
	cleanup();
	vi.clearAllMocks();
	vi.unstubAllGlobals();
});

it("an incomplete sign-out from the account menu goes to the retry page", async () => {
	const replace = vi.fn();
	vi.stubGlobal("location", { ...window.location, replace });
	render(
		<Wrap>
			<AdminLayout>content</AdminLayout>
		</Wrap>,
	);
	const triggers = screen.getAllByText(/Jane Doe|jane@example.com/);
	fireEvent.click(triggers[triggers.length - 1]);
	const item = await screen.findByText("Sign out");
	fireEvent.click(item);
	await waitFor(() => expect(logout).toHaveBeenCalled());
	await waitFor(() => expect(replace).toHaveBeenCalledWith("/auth/logout"));
	// And NOT to the page a finished sign-out lands on, which is where the route
	// shell sends an unauthenticated viewer: the two are indistinguishable to
	// the person, and only one of them is true.
	expect(routerReplace).not.toHaveBeenCalledWith("/auth/login");
});

// THE CONTROL. Without it the assertion above would also pass on a menu that
// sent every sign-out to the retry page, which presents a finished sign-out as
// unfinished — the same defect in the other direction.
it("a complete sign-out from the account menu goes to the signed-out page", async () => {
	logout.mockResolvedValueOnce({ complete: true });
	const replace = vi.fn();
	vi.stubGlobal("location", { ...window.location, replace });
	render(
		<Wrap>
			<AdminLayout>content</AdminLayout>
		</Wrap>,
	);
	const triggers = screen.getAllByText(/Jane Doe|jane@example.com/);
	fireEvent.click(triggers[triggers.length - 1]);
	fireEvent.click(await screen.findByText("Sign out"));
	await waitFor(() => expect(replace).toHaveBeenCalledWith("/auth/login"));
});
