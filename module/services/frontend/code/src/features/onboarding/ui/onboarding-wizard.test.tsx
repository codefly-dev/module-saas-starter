import { Code, ConnectError } from "@connectrpc/connect";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { useSyncExternalStore } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { OnboardingController } from "../application/controller";
import { OnboardingWizardView } from "./onboarding-wizard";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ user: { email: "user@example.com" } }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
afterEach(cleanup);
function View({ controller }: { controller: OnboardingController }) {
	const model = useSyncExternalStore(
		controller.subscribe,
		controller.getSnapshot,
	);
	return <OnboardingWizardView controller={controller} model={model} />;
}
it("validates beside kit fields and locks the draft during submission", async () => {
	let fail!: (error: Error) => void;
	const create = vi.fn(
		() =>
			new Promise<string>((_resolve, reject) => {
				fail = reject;
			}),
	);
	const controller = new OnboardingController({
		backend: {
			createOrganization: create,
			switchOrganization: vi.fn(),
			getProgress: vi.fn(),
			skipStep: vi.fn(),
		},
	});
	render(<View controller={controller} />);
	fireEvent.click(screen.getByRole("button", { name: "Create organization" }));
	expect(
		screen.getByLabelText("Organization name").getAttribute("aria-invalid"),
	).toBe("true");
	expect(create).not.toHaveBeenCalled();
	fireEvent.change(screen.getByLabelText("Organization name"), {
		target: { value: "Acme" },
	});
	fireEvent.click(screen.getByRole("button", { name: "Create organization" }));
	expect(
		(screen.getByLabelText("Organization name") as HTMLInputElement).disabled,
	).toBe(true);
	expect(
		(screen.getByLabelText("Organization address") as HTMLInputElement)
			.disabled,
	).toBe(true);
	fail(new Error("private-server-detail"));
	expect(await screen.findByText("Setup wasn’t saved")).toBeTruthy();
	expect(
		(screen.getByLabelText("Organization name") as HTMLInputElement).value,
	).toBe("Acme");
	expect(screen.queryByText("private-server-detail")).toBeNull();
});
it("shows refused setup access without offering a futile retry", async () => {
	const controller = new OnboardingController({
		organizationId: "org-1",
		backend: {
			createOrganization: vi.fn(),
			switchOrganization: vi.fn(),
			getProgress: vi
				.fn()
				.mockRejectedValue(
					new ConnectError("private-server-detail", Code.PermissionDenied),
				),
			skipStep: vi.fn(),
		},
	});
	render(<View controller={controller} />);
	await act(async () => {
		await controller.start();
	});
	await waitFor(() =>
		expect(
			screen.getByText(/Ask your organization administrator/),
		).toBeTruthy(),
	);
	expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
	expect(screen.queryByText("private-server-detail")).toBeNull();
});
