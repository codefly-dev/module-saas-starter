import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { describe, expect, it, vi } from "vitest";

import { InvitationService } from "@/gen/saas/accounts/v1/invitations_pb";

import {
	isMFARequired,
	refusalMessage,
	refusalToast,
	STEP_UP_MESSAGE,
	stepUpHref,
} from "./step-up";

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError } }));

/**
 * The error the browser builds from a refusal accounts actually sends: the body
 * is the one accounts' TestConnectRefusalCarriesTheStatusMessage pins, decoded
 * by the same Connect transport the portal uses.
 */
async function refusalFromTheWire(body: unknown): Promise<unknown> {
	const transport = createConnectTransport({
		baseUrl: "https://host.example.com",
		fetch: async () =>
			new Response(JSON.stringify(body), {
				status: 400,
				headers: { "Content-Type": "application/json" },
			}),
	});
	try {
		await createClient(InvitationService, transport).createInvitation({});
	} catch (error) {
		return error;
	}
	throw new Error("the call was expected to be refused");
}

describe("isMFARequired", () => {
	it("recognises the refusal accounts sends", async () => {
		const error = await refusalFromTheWire({
			code: "failed_precondition",
			message: "mfa_required",
		});
		expect(isMFARequired(error)).toBe(true);
	});

	it("recognises the host's refusal as a Connect error", () => {
		expect(
			isMFARequired(new ConnectError("mfa_required", Code.FailedPrecondition)),
		).toBe(true);
		expect(
			isMFARequired(new ConnectError("mfa_required", Code.PermissionDenied)),
		).toBe(false);
		expect(
			isMFARequired(new ConnectError("quota", Code.FailedPrecondition)),
		).toBe(false);
	});

	it("recognises the refusal once it has been flattened to a message", () => {
		expect(
			isMFARequired(
				new Error(
					"[failed_precondition] rpc error: code = FailedPrecondition desc = mfa_required",
				),
			),
		).toBe(true);
		expect(isMFARequired(new Error("mfa_required"))).toBe(false);
		expect(isMFARequired(new Error("network down"))).toBe(false);
	});
});

describe("stepUpHref", () => {
	it("sends the person to sign in and back to where they were", () => {
		expect(stepUpHref("/admin/platform/admins?tab=1")).toBe(
			"/auth/login?step_up=1&next=%2Fadmin%2Fplatform%2Fadmins%3Ftab%3D1",
		);
	});
});

describe("refusalMessage", () => {
	it.each([
		Code.InvalidArgument,
		Code.NotFound,
		Code.AlreadyExists,
		Code.PermissionDenied,
		Code.Unauthenticated,
		Code.FailedPrecondition,
		Code.ResourceExhausted,
		Code.Internal,
	])("keeps server diagnostics off screen for %s", (code) => {
		const message = refusalMessage(
			new ConnectError("private server detail and permission name", code),
			"Couldn't save the change.",
		);
		expect(message).not.toContain("private server");
		expect(message).not.toContain("permission name");
		if (code === Code.PermissionDenied) {
			expect(message).toContain("organization administrator");
			expect(message).not.toContain("Try again");
		}
	});
	it("explains an existing item from a real transport refusal", async () => {
		const error = await refusalFromTheWire({
			code: "already_exists",
			message: "private invitation details",
		});
		expect(refusalMessage(error, "Couldn't invite this person.")).toContain(
			"An item with these details already exists.",
		);
	});
	it("preserves passkey confirmation", () => {
		expect(
			refusalMessage(
				new ConnectError("mfa_required", Code.FailedPrecondition),
				"Couldn't save.",
			),
		).toBe(STEP_UP_MESSAGE);
	});
	it("uses the screen's fallback for network failures", () => {
		expect(
			refusalMessage(new Error("private endpoint"), "Couldn't save."),
		).toBe("Couldn't save.");
	});
});

describe("refusalToast", () => {
	it("shows safe validation guidance", () => {
		toastError.mockClear();
		refusalToast(
			new ConnectError("private field details", Code.InvalidArgument),
			"Couldn't save.",
		);
		expect(toastError).toHaveBeenCalledWith(
			"Couldn't make this change. Check the information you entered.",
		);
	});
	it("preserves the passkey action", () => {
		toastError.mockClear();
		refusalToast(
			new ConnectError("mfa_required", Code.FailedPrecondition),
			"Couldn't save.",
		);
		expect(toastError).toHaveBeenCalledWith(
			STEP_UP_MESSAGE,
			expect.objectContaining({
				action: expect.objectContaining({ label: "Confirm passkey" }),
			}),
		);
	});
});
