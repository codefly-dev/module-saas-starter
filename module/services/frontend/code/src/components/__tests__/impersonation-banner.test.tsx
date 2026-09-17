import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ImpersonationBanner } from "../impersonation-banner";

const { exitImpersonation, toastError } = vi.hoisted(() => ({
	exitImpersonation: vi.fn(),
	toastError: vi.fn(),
}));

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({
		impersonation: {
			isImpersonating: true,
			subjectId: "019f6c01-0002-7000-8000-000000000002",
			impersonatorId: "019f6c01-0001-7000-8000-000000000001",
		},
		user: { email: "support@example.com" },
		exitImpersonation,
	}),
}));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

afterEach(() => {
	cleanup();
	exitImpersonation.mockReset();
	toastError.mockReset();
});

describe("Impersonation banner", () => {
	it("stops the session through the auth context", async () => {
		exitImpersonation.mockResolvedValue(undefined);
		render(<ImpersonationBanner />);

		fireEvent.click(
			screen.getByRole("button", { name: /stop impersonating/i }),
		);

		await waitFor(() => expect(exitImpersonation).toHaveBeenCalledTimes(1));
		expect(toastError).not.toHaveBeenCalled();
	});

	// The operator is back on their own session either way, so the only thing
	// left to get right is telling them the server-side stop did not take —
	// a silent failure here is indistinguishable from the client-only exit.
	it("reports a stop that did not take effect on the server", async () => {
		exitImpersonation.mockRejectedValue(
			new Error("the impersonation session could not be ended on the server"),
		);
		render(<ImpersonationBanner />);

		fireEvent.click(
			screen.getByRole("button", { name: /stop impersonating/i }),
		);

		await waitFor(() =>
			expect(toastError).toHaveBeenCalledWith(
				"the impersonation session could not be ended on the server",
			),
		);
	});
});
