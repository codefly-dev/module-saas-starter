import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
	isAuthenticated: false,
	isLoading: false,
}));

const clients = vi.hoisted(() => ({
	consent: {
		getStatus: vi.fn(),
		updatePreferences: vi.fn(),
		acceptTerms: vi.fn(),
	},
	acquisition: {
		getAcquisitionStatus: vi.fn(),
	},
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => authState,
}));

vi.mock("@/lib/legal-config", () => ({
	legalContentConfigured: () => false,
}));

vi.mock("@connectrpc/connect", () => ({
	createClient: vi.fn((service: { typeName: string }) =>
		service.typeName.endsWith("ConsentService")
			? clients.consent
			: clients.acquisition,
	),
}));

import { ConsentBanner } from "./consent-banner";

afterEach(() => {
	cleanup();
	window.localStorage.clear();
	vi.clearAllMocks();
	authState.isAuthenticated = false;
	authState.isLoading = false;
});

describe("ConsentBanner", () => {
	it("does not allow Terms acceptance while legal content is unconfigured", async () => {
		authState.isAuthenticated = true;
		clients.consent.getStatus.mockResolvedValue({
			currentTermsVersion: "terms-v1",
			termsAcceptedVersion: "",
			policyVersion: "policy-v2",
			purposes: [],
		});

		render(<ConsentBanner />);

		const acceptTerms = await screen.findByRole("button", {
			name: "Accept Terms",
		});
		expect(acceptTerms.getAttribute("disabled")).not.toBeNull();
	});

	it("persists the current server policy version for anonymous preferences", async () => {
		clients.acquisition.getAcquisitionStatus.mockResolvedValue({
			consentPolicyVersion: "policy-v2",
		});

		render(<ConsentBanner />);
		fireEvent.click(
			await screen.findByRole("button", { name: "Reject optional" }),
		);

		await waitFor(() =>
			expect(
				JSON.parse(
					window.localStorage.getItem("saas-starter:consent-preferences") ??
						"{}",
				),
			).toEqual({
				policyVersion: "policy-v2",
				analytics: false,
				marketing: false,
			}),
		);
	});
});
