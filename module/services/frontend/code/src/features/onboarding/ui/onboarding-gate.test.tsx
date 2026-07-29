import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
	isAuthenticated: true,
	organizationId: "",
	switchOrganization: vi.fn(),
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

vi.mock("next/navigation", () => ({
	usePathname: () => "/",
	useRouter: () => ({ replace: vi.fn() }),
}));

import { OnboardingGate } from "./onboarding-gate";

function Wrapper({ children }: { children: ReactNode }) {
	return (
		<QueryClientProvider
			client={
				new QueryClient({
					defaultOptions: { queries: { retry: false } },
				})
			}
		>
			{children}
		</QueryClientProvider>
	);
}

afterEach(() => {
	cleanup();
	window.sessionStorage.clear();
});

describe("OnboardingGate", () => {
	it("lets an authenticated user without an organization create one", () => {
		render(
			<Wrapper>
				<OnboardingGate>
					<div>dashboard content</div>
				</OnboardingGate>
			</Wrapper>,
		);

		expect(screen.getByText("Create your workspace")).toBeTruthy();
		expect(screen.getByLabelText("Organization name")).toBeTruthy();
		expect(
			screen.getByRole("button", { name: "Create organization" }),
		).toBeTruthy();
		expect(screen.queryByText("dashboard content")).toBeNull();
	});
});
