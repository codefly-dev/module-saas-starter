import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { MFASetup } from "./mfa-setup";

afterEach(cleanup);

const SECRET = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";
const PROVISIONING_URI = `otpauth://totp/Example:jane.doe@example.com?secret=${SECRET}&issuer=Example&digits=6&period=30`;

describe("MFASetup enrolment step", () => {
	it("draws the code the provisioning URI encodes, and writes the secret out beside it", async () => {
		server.use(
			http.post(rpc("MFAService", "SetupTOTP"), () =>
				HttpResponse.json({
					secret: SECRET,
					provisioningUri: PROVISIONING_URI,
				}),
			),
		);

		renderInApp(<MFASetup open onClose={() => undefined} />);

		// The code is an image a reader can be told about, not a placeholder box:
		// the grid it draws has to be the one this URI encodes, so its cell count
		// is the URI's and not some fixed figure's.
		const code = await screen.findByRole("img", { name: /scan this code/i });
		const viewBox = code.getAttribute("viewBox");
		expect(viewBox).toBe("0 0 53 53");
		// A reader who cannot scan has to be able to type it instead.
		expect(screen.getByText(SECRET)).toBeTruthy();
	});

	it("stops at one request when enrolment fails, and retries only when asked", async () => {
		// SetupTOTP writes another unverified device row per call, and the request
		// used to be made during render under a guard that a failure cleared — so
		// a failing endpoint was asked again on every render it caused. The count
		// is the assertion: against this same handler the previous version reached
		// five figures inside a second.
		let calls = 0;
		server.use(
			http.post(rpc("MFAService", "SetupTOTP"), () => {
				calls += 1;
				if (calls === 1) return HttpResponse.error();
				return HttpResponse.json({
					secret: SECRET,
					provisioningUri: PROVISIONING_URI,
				});
			}),
		);

		renderInApp(<MFASetup open onClose={() => undefined} />);

		// The failure is said where the code would have been, rather than leaving
		// a dialog that waits forever.
		expect(
			await screen.findByText(/could not prepare your authenticator code/i),
		).toBeTruthy();
		expect(screen.queryByRole("img", { name: /scan this code/i })).toBeNull();
		await waitFor(() => expect(calls).toBe(1));

		fireEvent.click(screen.getByRole("button", { name: "Try again" }));

		expect(
			await screen.findByRole("img", { name: /scan this code/i }),
		).toBeTruthy();
		expect(calls).toBe(2);
	});
});
