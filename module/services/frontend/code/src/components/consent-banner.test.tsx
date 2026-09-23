import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const authState = vi.hoisted(() => ({
	isAuthenticated: false,
	isLoading: false,
}));

const legalState = vi.hoisted(() => ({
	configured: false,
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

const logout = vi.hoisted(() => vi.fn(async () => undefined));

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		isAuthenticated: authState.isAuthenticated,
		isLoading: authState.isLoading,
		logout,
	}),
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
	legalState.configured = false;
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

		render(<ConsentBanner legalConfigured={legalState.configured} />);

		const acceptTerms = await screen.findByRole("button", {
			name: "Accept Terms",
		});
		expect(acceptTerms.getAttribute("disabled")).not.toBeNull();
	});

	it("offers a way to sign out while the Terms are pending", async () => {
		authState.isAuthenticated = true;
		clients.consent.getStatus.mockResolvedValue({
			currentTermsVersion: "terms-v1",
			termsAcceptedVersion: "",
			policyVersion: "policy-v2",
			purposes: [],
		});
		const replace = vi.fn();
		vi.stubGlobal("location", { ...window.location, replace });

		render(<ConsentBanner legalConfigured={legalState.configured} />);

		fireEvent.click(await screen.findByRole("button", { name: "Sign out" }));
		await waitFor(() => expect(replace).toHaveBeenCalledWith("/auth/login"));
		expect(logout).toHaveBeenCalledTimes(1);
		vi.unstubAllGlobals();
	});

	it("keeps Sign out enabled, first and reachable when Terms cannot be accepted", async () => {
		authState.isAuthenticated = true;
		clients.consent.getStatus.mockResolvedValue({
			currentTermsVersion: "terms-v1",
			termsAcceptedVersion: "",
			policyVersion: "policy-v2",
			purposes: [],
		});

		render(<ConsentBanner legalConfigured={false} />);

		const notice = await screen.findByRole("dialog", {
			name: "Review the current Terms",
		});
		expect(notice.getAttribute("aria-modal")).toBe("false");
		const buttons = within(notice).getAllByRole("button");
		expect(buttons.map((button) => button.textContent)).toEqual([
			"Sign out",
			"Accept Terms",
		]);
		const signOut = buttons[0];
		expect(signOut.hasAttribute("disabled")).toBe(false);
		signOut.focus();
		expect(document.activeElement).toBe(signOut);
		// The Terms state has no "later": its only way out is leaving the session.
		expect(
			within(notice).queryByRole("button", { name: "Decide later" }),
		).toBeNull();
	});

	it("lets a user put the preferences notice away for later", async () => {
		clients.acquisition.getAcquisitionStatus.mockResolvedValue({
			consentPolicyVersion: "policy-v2",
		});

		render(<ConsentBanner legalConfigured={legalState.configured} />);

		const notice = await screen.findByRole("dialog", {
			name: "Your privacy choices",
		});
		fireEvent.click(
			within(notice).getByRole("button", { name: "Decide later" }),
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(
			window.localStorage.getItem("saas-starter:consent-preferences"),
		).toBeNull();
	});

	it("allows Terms acceptance once legal content is configured", async () => {
		authState.isAuthenticated = true;
		legalState.configured = true;
		clients.consent.getStatus.mockResolvedValue({
			currentTermsVersion: "terms-v1",
			termsAcceptedVersion: "",
			policyVersion: "policy-v2",
			purposes: [],
		});
		clients.consent.acceptTerms.mockResolvedValue({
			policyVersion: "policy-v2",
			purposes: [],
		});

		render(<ConsentBanner legalConfigured={legalState.configured} />);

		const acceptTerms = await screen.findByRole("button", {
			name: "Accept Terms",
		});
		expect(acceptTerms.getAttribute("disabled")).toBeNull();

		fireEvent.click(acceptTerms);

		await waitFor(() =>
			expect(clients.consent.acceptTerms).toHaveBeenCalledWith({
				version: "terms-v1",
				context: "consent_banner",
			}),
		);
	});

	it("persists the current server policy version for anonymous preferences", async () => {
		clients.acquisition.getAcquisitionStatus.mockResolvedValue({
			consentPolicyVersion: "policy-v2",
		});

		render(<ConsentBanner legalConfigured={legalState.configured} />);
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
