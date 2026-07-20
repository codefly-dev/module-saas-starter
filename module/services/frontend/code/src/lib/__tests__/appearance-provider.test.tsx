import { defineFrontend } from "@codefly/saas-plugin-contract";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
	settings: {
		primaryColor: "#336699",
		logoUrl: "https://assets.example.test/logo.svg",
		faviconUrl: "",
	},
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		organizationId: "00000000-0000-4000-8000-000000000001",
		isAuthenticated: true,
		isLoading: false,
	}),
}));
vi.mock("@/features/organizations/service/queries", () => ({
	orgQueries: {
		settings: (orgId: string) => ({
			queryKey: ["org-settings", orgId],
			queryFn: async () => mocks.settings,
		}),
	},
}));

import { AppearanceProvider, useAppearance } from "../appearance-provider";

const config = defineFrontend({
	branding: {
		name: "Example",
		mark: "E",
		title: "Example",
		description: "Example",
	},
	appearance: { light: { primary: "#111111" } },
	plugins: [],
});

function Consumer() {
	const { branding, isTenantBranded } = useAppearance();
	return (
		<div>
			<span>{branding.logo?.lightSrc ?? "application-logo"}</span>
			<span>{isTenantBranded ? "tenant" : "application"}</span>
		</div>
	);
}

afterEach(() => {
	cleanup();
	document.documentElement.style.cssText = "";
	mocks.settings = {
		primaryColor: "#336699",
		logoUrl: "https://assets.example.test/logo.svg",
		faviconUrl: "",
	};
});

describe("AppearanceProvider", () => {
	it("applies only the supported tenant overlay and restores application tokens", async () => {
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		const rendered = render(
			<QueryClientProvider client={queryClient}>
				<AppearanceProvider config={config}>
					<Consumer />
				</AppearanceProvider>
			</QueryClientProvider>,
		);

		await waitFor(() =>
			expect(
				document.documentElement.style.getPropertyValue(
					"--appearance-light-primary",
				),
			).toBe("#336699"),
		);
		expect(
			screen.getByText("https://assets.example.test/logo.svg"),
		).toBeTruthy();
		expect(screen.getByText("tenant")).toBeTruthy();

		rendered.unmount();
		expect(
			document.documentElement.style.getPropertyValue(
				"--appearance-light-primary",
			),
		).toBe("");
	});

	it("ignores unsafe tenant values", async () => {
		mocks.settings = {
			primaryColor: "red; color: transparent",
			logoUrl: "javascript:alert(1)",
			faviconUrl: "data:text/html,unsafe",
		};
		const queryClient = new QueryClient({
			defaultOptions: { queries: { retry: false } },
		});
		render(
			<QueryClientProvider client={queryClient}>
				<AppearanceProvider config={config}>
					<Consumer />
				</AppearanceProvider>
			</QueryClientProvider>,
		);

		await waitFor(() =>
			expect(screen.getByText("application-logo")).toBeTruthy(),
		);
		expect(screen.getByText("application")).toBeTruthy();
		expect(
			document.documentElement.style.getPropertyValue(
				"--appearance-light-primary",
			),
		).toBe("");
	});
});
