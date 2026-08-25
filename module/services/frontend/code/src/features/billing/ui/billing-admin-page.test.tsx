import { cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1", getToken: () => "test-token" }),
}));

import { BillingAdminPage } from "./billing-admin-page";

afterEach(cleanup);

describe("BillingAdminPage admin container", () => {
	it("renders an invoice the billing service returns", async () => {
		server.use(
			http.post(rpc("PlatformAdminService", "GetOrgEntitlements"), () =>
				HttpResponse.json({ planName: "Pro", entitlements: [] }),
			),
			http.post(rpc("UsageService", "ListUsageMeters"), () =>
				HttpResponse.json({ meters: [] }),
			),
			http.post(rpc("BillingService", "ListInvoices"), () =>
				HttpResponse.json({
					invoices: [
						{
							id: "in_123456789012",
							number: "INV-001",
							status: "paid",
							amountPaid: "2000",
							amountDue: "0",
							currency: "usd",
							hostedInvoiceUrl: "",
							invoicePdf: "",
						},
					],
				}),
			),
		);

		renderInApp(<BillingAdminPage />);

		expect(await screen.findByText("INV-001")).toBeTruthy();
	});
});
