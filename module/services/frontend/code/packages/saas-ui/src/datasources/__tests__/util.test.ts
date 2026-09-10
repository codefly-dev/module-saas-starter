import { describe, expect, it } from "vitest";
import { formatGrants } from "../util.js";

describe("formatGrants", () => {
	it("orders read before write regardless of input order", () => {
		expect(formatGrants(["write", "read"])).toBe("Read · Write");
	});

	it("sorts an unrecognized action last, not first", () => {
		// indexOf returns -1 for an unknown action, which would rank it ahead of
		// read and present the least-understood grant as the primary one.
		expect(formatGrants(["admin", "read"])).toBe("Read · Admin");
	});

	it("renders a single grant on its own", () => {
		expect(formatGrants(["read"])).toBe("Read");
	});
});
