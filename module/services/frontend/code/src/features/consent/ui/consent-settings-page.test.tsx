import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { ConsentSettingsPage } from "./consent-settings-page";

afterEach(cleanup);

describe("ConsentSettingsPage admin container", () => {
	it("renders the policy version the consent service returns", async () => {
		server.use(
			http.post(rpc("ConsentService", "GetStatus"), () =>
				HttpResponse.json({
					policyVersion: "2025-01",
					purposes: [
						{ purpose: 2, granted: true },
						{ purpose: 3, granted: false },
					],
				}),
			),
		);

		renderInApp(<ConsentSettingsPage />);

		expect(await screen.findByText("Policy version 2025-01")).toBeTruthy();
	});

	// A failed read used to leave both switches reading "off" with nothing said:
	// a default passing for the person's answer, one save away from withdrawing
	// consent they had given.
	it("says the choices could not be loaded and offers nothing to save", async () => {
		server.use(
			http.post(rpc("ConsentService", "GetStatus"), () =>
				HttpResponse.json(
					{ code: "unavailable", message: "upstream connect error" },
					{ status: 503 },
				),
			),
		);

		renderInApp(<ConsentSettingsPage />);

		expect(
			await screen.findByText("Couldn't load your privacy choices"),
		).toBeTruthy();
		expect(screen.queryByText(/upstream connect error/)).toBeNull();
		expect(
			screen.queryByRole("switch", { name: "Allow analytics" }),
		).toBeNull();
		expect(screen.queryByRole("button", { name: "Save choices" })).toBeNull();
	});
});

it("keeps choices locked during saving and preserves the draft after refusal", async () => {
	let finish!: () => void;
	let writes = 0;
	server.use(
		http.post(rpc("ConsentService", "GetStatus"), () =>
			HttpResponse.json({ policyVersion: "2025-01", purposes: [] }),
		),
		http.post(rpc("ConsentService", "UpdatePreferences"), async () => {
			writes++;
			await new Promise<void>((resolve) => {
				finish = resolve;
			});
			return HttpResponse.json(
				{ code: "permission_denied", message: "private rule" },
				{ status: 403 },
			);
		}),
	);
	renderInApp(<ConsentSettingsPage />);
	const analytics = await screen.findByRole("switch", {
		name: "Allow analytics",
	});
	fireEvent.click(analytics);
	fireEvent.click(screen.getByRole("button", { name: "Save choices" }));
	await waitFor(() => expect(writes).toBe(1));
	expect(analytics.getAttribute("aria-disabled")).toBe("true");
	expect(
		screen
			.getByRole("switch", { name: "Allow marketing" })
			.getAttribute("aria-disabled"),
	).toBe("true");
	finish();
	expect(
		await screen.findByText(/Ask your organization administrator for access/),
	).toBeTruthy();
	expect(analytics.getAttribute("aria-checked")).toBe("true");
	expect(screen.queryByText(/private rule/)).toBeNull();
});
it("explains a denied read without suggesting another attempt", async () => {
	server.use(
		http.post(rpc("ConsentService", "GetStatus"), () =>
			HttpResponse.json(
				{ code: "permission_denied", message: "private rule" },
				{ status: 403 },
			),
		),
	);
	renderInApp(<ConsentSettingsPage />);
	expect(
		await screen.findByText(/Ask your organization administrator for access/),
	).toBeTruthy();
	expect(screen.queryByRole("switch")).toBeNull();
	expect(screen.queryByText(/Try again/)).toBeNull();
});
