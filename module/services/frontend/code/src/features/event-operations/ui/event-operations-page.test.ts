import { describe, expect, it } from "vitest";
import { eventVisibilityLabel } from "./event-operations-page";

describe("eventVisibilityLabel", () => {
	it("labels the declared catalog visibilities and undeclared types", () => {
		expect(
			["internal", "tenant", "external", ""].map(eventVisibilityLabel),
		).toEqual(["Internal", "Tenant", "External", "Undeclared"]);
	});

	it("passes through an unrecognized visibility verbatim", () => {
		expect(eventVisibilityLabel("restricted")).toBe("restricted");
	});
});
