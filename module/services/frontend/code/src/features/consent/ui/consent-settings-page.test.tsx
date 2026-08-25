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
});
