import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import BillingSuccessClient from "./billing-success-client";
afterEach(cleanup);
it("offers verification without claiming an unverified subscription is active", () => {
	render(<BillingSuccessClient />);
	expect(
		screen.getByRole("heading", { name: "Review your subscription" }),
	).toBeTruthy();
	expect(
		screen.getByRole("link", { name: "View billing" }).getAttribute("href"),
	).toBe("/admin/billing");
	expect(screen.queryByText("Subscription active")).toBeNull();
});
