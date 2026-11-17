import { cleanup, screen } from "@testing-library/react";
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
