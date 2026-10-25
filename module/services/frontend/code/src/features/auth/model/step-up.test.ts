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
	it("shows the server's own words when the organization would lose its last administrator", () => {
		expect(
			refusalMessage(
				new ConnectError(
					"organization must keep at least one owner or admin",
					Code.FailedPrecondition,
				),
				"Failed to remove member",
			),
		).toBe("organization must keep at least one owner or admin");
	});

	// The host sends the quota's own sentence, never the wrapped call path, so
	// the person learns which limit they hit.
	it("shows the quota the request hit", () => {
		expect(
			refusalMessage(
				new ConnectError(
					"seats quota exhausted: used 5 of 5",
					Code.ResourceExhausted,
				),
				"Failed to add member",
			),
		).toBe("seats quota exhausted: used 5 of 5");
	});

	it.each([
		[Code.InvalidArgument, "email: value must be a valid email address"],
		[Code.NotFound, "invitation unavailable"],
		[
			Code.AlreadyExists,
			"this address already has a pending invitation; find it in the invitations list to copy its link or resend it",
		],
		[Code.PermissionDenied, "this action needs a higher platform role"],
		[Code.Unauthenticated, "sign in again to continue"],
	])("shows the host's reason under %s", (code, message) => {
		expect(
			refusalMessage(new ConnectError(message, code), "Failed to invite"),
		).toBe(message);
	});

	it("shows the reason accounts sends for a duplicate invitation", async () => {
		const error = await refusalFromTheWire({
			code: "already_exists",
			message: "this address already has a pending invitation",
		});
		expect(refusalMessage(error, "Failed to create invitation")).toBe(
			"this address already has a pending invitation",
		);
	});

	it("never shows the bare step-up reason", () => {
		expect(
			refusalMessage(
				new ConnectError("mfa_required", Code.FailedPrecondition),
				"Failed to create key",
			),
		).toBe(STEP_UP_MESSAGE);
	});

	it("does not put server internals in front of the user", () => {
		expect(
			refusalMessage(
				new ConnectError(
					'cannot remove member: pq: connection refused "10.0.0.4:5432"',
					Code.Internal,
				),
				"Failed to remove member",
			),
		).toBe("Failed to remove member");
	});

	it("falls back for an error that never reached the server", () => {
		expect(
			refusalMessage(new Error("network down"), "Failed to add member"),
		).toBe("Failed to add member");
	});

	it("falls back when the server sent no message to show", () => {
		expect(
			refusalMessage(
				new ConnectError("", Code.FailedPrecondition),
				"Failed to remove member",
			),
		).toBe("Failed to remove member");
	});
});

describe("refusalToast", () => {
	it("shows the host's reason rather than the screen's fallback", () => {
		toastError.mockClear();
		refusalToast(
			new ConnectError(
				"impersonation requires a justification",
				Code.InvalidArgument,
			),
			"Failed to impersonate user",
		);
		expect(toastError).toHaveBeenCalledWith(
			"impersonation requires a justification",
		);
	});

	it("falls back for a fault", () => {
		toastError.mockClear();
		refusalToast(
			new ConnectError("the request could not be completed", Code.Internal),
			"Failed to revoke role",
		);
		expect(toastError).toHaveBeenCalledWith("Failed to revoke role");
	});
});
