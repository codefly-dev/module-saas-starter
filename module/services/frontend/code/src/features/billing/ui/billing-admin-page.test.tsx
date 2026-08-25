import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/setup";

vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "org-1", getToken: () => "test-token" }),
}));

import { BillingAdminPage } from "./billing-admin-page";

function rpc(service: string, method: string) {
	return `http://localhost:3000/saas.accounts.v1.${service}/${method}`;
}

function renderInApp(ui: React.ReactElement) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>{ui}</QueryClientProvider>,
	);
}

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
