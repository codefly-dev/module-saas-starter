import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import CompliancePage from "./page";

vi.mock("server-only", () => ({}));

describe("CompliancePage", () => {
	it("renders starter state and adopter responsibilities without unsupported claims", () => {
		render(<CompliancePage />);

		expect(
			screen.getByText("Starter defaults are not production evidence."),
		).toBeTruthy();
		fireEvent.click(
			screen.getByRole("button", {
				name: /Production and adopter responsibilities/,
			}),
		);
		expect(
			screen.getAllByText("Adopter action required").length,
		).toBeGreaterThan(0);
		expect(
			screen.getAllByText("Starter implementation").length,
		).toBeGreaterThan(0);
		expect(screen.queryByText(/backups are retained for 90 days/i)).toBeNull();
		expect(screen.queryByText(/DPA is available upon request/i)).toBeNull();
		expect(
			screen.queryByText(/annual third-party penetration testing/i),
		).toBeNull();
		expect(screen.queryByText(/target response: 15 minutes/i)).toBeNull();
	});
});
