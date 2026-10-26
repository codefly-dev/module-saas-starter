import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { QRCode } from "@/shared/ui";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { MFASetup } from "./mfa-setup";

afterEach(cleanup);

/**
 * The real wire shape, not a hand-shortened one: `totpSecretBytes = 32` in the
 * accounts service, base32-encoded without padding, is 52 characters. A 32
 * character stand-in encodes to a smaller, sparser grid, so a fixture written
 * that way renders a code the product never produces — which is how the
 * original version of this test certified a density the reader never gets.
 */
const SECRET = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXPJBSW";
const PROVISIONING_URI = `otpauth://totp/SaaSStarter:jane.doe@example.com?secret=${SECRET}&issuer=SaaSStarter&digits=6&period=30`;

function setupReturns(secret: string, uri: string) {
	return http.post(rpc("MFAService", "SetupTOTP"), () =>
		HttpResponse.json({ secret, provisioningUri: uri }),
	);
}

/**
 * The module path of a rendered code, which is a function of exactly what was
 * encoded. Taken from the `svg` itself because the dialog renders through a
 * portal, so it is never inside the render root.
 */
function modules(code: Element): string {
	const paths = [...code.querySelectorAll("path")];
	return paths.at(-1)?.getAttribute("d") ?? "";
}

describe("MFASetup enrolment step", () => {
	it("draws the code the provisioning URI encodes, and writes the secret out beside it", async () => {
		server.use(setupReturns(SECRET, PROVISIONING_URI));

		renderInApp(<MFASetup open onClose={() => undefined} />);

		const code = await screen.findByRole("img", { name: /scan this code/i });
		expect(code.tagName.toLowerCase()).toBe("svg");

		// What it drew is compared against the same kit component fed the URI
		// directly, rather than against a remembered grid size: this proves the
		// dialog handed the encoder the *provisioning URI* — not the bare secret,
		// not a stale value — and it stays true when the encoder picks a different
		// version for a different payload.
		const reference = render(
			<QRCode value={PROVISIONING_URI} label="reference" size="lg" />,
		);
		const referenceCode = reference.container.querySelector("svg");
		expect(referenceCode).toBeTruthy();
		expect(modules(code)).not.toBe("");
		expect(modules(code)).toBe(modules(referenceCode as Element));

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

	it("starts a fresh enrolment when a parent reopens it by prop", async () => {
		// Base UI reports only a close the reader performed, so a parent that
		// closes this by flipping `open` never reaches `onOpenChange`. When the
		// session state hung off that callback, reopening kept the first
		// enrolment's code and secret on screen and made no new request — and a
		// dialog closed on the backup-codes step reopened still showing them.
		const secrets = [SECRET, `ZZZZ${SECRET.slice(4)}`];
		let calls = 0;
		server.use(
			http.post(rpc("MFAService", "SetupTOTP"), () => {
				const secret = secrets[Math.min(calls, secrets.length - 1)];
				calls += 1;
				return HttpResponse.json({
					secret,
					provisioningUri: PROVISIONING_URI.replace(SECRET, secret),
				});
			}),
		);

		const { rerender } = renderInApp(
			<MFASetup open onClose={() => undefined} />,
		);
		expect(await screen.findByText(secrets[0])).toBeTruthy();

		rerender(<MFASetup open={false} onClose={() => undefined} />);
		rerender(<MFASetup open onClose={() => undefined} />);

		expect(await screen.findByText(secrets[1])).toBeTruthy();
		expect(screen.queryByText(secrets[0])).toBeNull();
		expect(calls).toBe(2);
	});
});
