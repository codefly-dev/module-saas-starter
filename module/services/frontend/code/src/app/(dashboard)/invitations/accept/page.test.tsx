import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
	mutate: vi.fn(),
	replace: vi.fn(),
	searchParams: new URLSearchParams(),
}));

vi.mock("next/navigation", () => ({
	useRouter: () => ({ replace: mocks.replace }),
	useSearchParams: () => mocks.searchParams,
}));

vi.mock("@/features/invitations/service/mutations", () => ({
	useAcceptInvitation: () => ({
		error: null,
		isPending: false,
		mutate: mocks.mutate,
	}),
}));

import Page from "./page";

beforeEach(() => {
	mocks.searchParams = new URLSearchParams("token=invite-token");
});

afterEach(() => {
	cleanup();
	mocks.mutate.mockClear();
	mocks.replace.mockClear();
});

describe("invitation acceptance route", () => {
	it("submits the link token and returns to the product after acceptance", () => {
		render(<Page />);

		fireEvent.click(screen.getByRole("button", { name: "Accept invitation" }));

		expect(mocks.mutate).toHaveBeenCalledWith(
			"invite-token",
			expect.objectContaining({ onSuccess: expect.any(Function) }),
		);
		const options = mocks.mutate.mock.calls[0]?.[1] as {
			onSuccess: () => void;
		};
		options.onSuccess();
		expect(mocks.replace).toHaveBeenCalledWith("/");
	});

	it("rejects a link without a token before calling the API", () => {
		mocks.searchParams = new URLSearchParams();
		render(<Page />);

		expect(
			screen.getByText("This invitation link is missing its token."),
		).toBeTruthy();
		expect(
			screen.getByRole("button", { name: "Accept invitation" }),
		).toHaveProperty("disabled", true);
		expect(mocks.mutate).not.toHaveBeenCalled();
	});
});
