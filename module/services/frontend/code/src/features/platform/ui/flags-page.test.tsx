import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../service/queries", () => ({
	useFeatureFlags: () => ({
		data: [
			{
				name: "legacy-dashboard",
				description: "Legacy dashboard rollout",
				enabled: true,
				rolloutPercent: 50,
				targetOrgIds: [],
			},
		],
		isLoading: false,
	}),
}));

import { FlagsPage } from "./flags-page";

afterEach(cleanup);

describe("FlagsPage", () => {
	it("presents the legacy rows as a read-only migration inventory", () => {
		render(<FlagsPage />);

		expect(screen.getByText("legacy-dashboard")).toBeTruthy();
		expect(screen.getByText(/read-only migration inventory/i)).toBeTruthy();
		expect(screen.queryByRole("button", { name: /create flag/i })).toBeNull();
		expect(screen.queryByRole("switch")).toBeNull();
	});
});
