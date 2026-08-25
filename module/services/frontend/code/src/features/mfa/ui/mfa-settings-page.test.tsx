import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { MFASettingsPage } from "./mfa-settings-page";

afterEach(cleanup);

describe("MFASettingsPage admin container", () => {
	it("renders the enrolled device the MFA service returns", async () => {
		// Real ListDevices wire shape: proto field names (deviceType enum,
		// Timestamp fields as RFC3339 strings) — NOT the domain MFADevice shape.
		server.use(
			http.post(rpc("MFAService", "ListDevices"), () =>
				HttpResponse.json({
					devices: [
						{
							id: "device-1",
							userId: "user-1",
							name: "Antoine's iPhone",
							deviceType: 2,
							createdAt: "2026-01-01T00:00:00Z",
							lastUsedAt: "2026-02-01T00:00:00Z",
						},
					],
				}),
			),
		);

		renderInApp(<MFASettingsPage />);

		expect(await screen.findByText("Antoine's iPhone")).toBeTruthy();
	});
});
