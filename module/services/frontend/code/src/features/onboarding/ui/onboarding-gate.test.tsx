import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
	isAuthenticated: true,
	organizationId: "",
	switchOrganization: vi.fn(),
	logout: vi.fn(),
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

vi.mock("next/navigation", () => ({
	usePathname: () => "/",
	useRouter: () => ({ replace: vi.fn() }),
}));

import { rpc } from "@/test/container";
import { server } from "@/test/setup";
import { OnboardingGate } from "./onboarding-gate";

function listOrganizations(body: {
	organizations: { id: string; name: string; slug: string }[];
	canCreate: boolean;
}) {
	server.use(
		http.post(rpc("OrganizationService", "ListOrganizations"), () =>
			HttpResponse.json(body),
		),
	);
}

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

	it("offers an organization the user already belongs to instead of only creating one", async () => {
		listOrganizations({
			organizations: [
				{ id: "personal", name: "Personal", slug: "personal-1a2b3c" },
				{ id: "acme", name: "Acme", slug: "acme" },
			],
			canCreate: true,
		});
		render(
			<Wrapper>
				<OnboardingGate>
					<div>dashboard content</div>
				</OnboardingGate>
			</Wrapper>,
		);

		expect(
			await screen.findByRole("button", { name: "Continue in Acme" }),
		).toBeTruthy();
		// The personal organization is not offered as a workspace.
		expect(
			screen.queryByRole("button", { name: "Continue in Personal" }),
		).toBeNull();
	});

	it("never walks a user into creating an organization the deployment forbids", async () => {
		listOrganizations({
			organizations: [
				{ id: "personal", name: "Personal", slug: "personal-1a2b3c" },
			],
			canCreate: false,
		});
		render(
			<Wrapper>
				<OnboardingGate>
					<div>dashboard content</div>
				</OnboardingGate>
			</Wrapper>,
		);

		expect(
			await screen.findByText("You're not in an organization yet"),
		).toBeTruthy();
		expect(screen.queryByLabelText("Organization name")).toBeNull();
		expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy();
	});
});
