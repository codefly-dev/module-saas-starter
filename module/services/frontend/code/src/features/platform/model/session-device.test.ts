import { describe, expect, it } from "vitest";
import { sessionDevice } from "./session-device";

describe("sessionDevice", () => {
	it.each([
		[
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/130.0 Safari/537.36",
			"Chrome on macOS",
		],
		[
			"Mozilla/5.0 (Windows NT 10.0) Chrome/130.0 Safari/537.36 Edg/130.0",
			"Edge on Windows",
		],
		[
			"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0) AppleWebKit/605.1 Safari/604.1",
			"Safari on iOS",
		],
		["Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0", "Firefox on Linux"],
		["unrecognized unbounded server string", "Unknown device"],
	])("summarizes %s", (browser, expected) => {
		expect(sessionDevice({ browser })).toBe(expected);
	});
	it("recognizes the description returned for a signed-in session", () =>
		expect(
			sessionDevice({
				description: "Mozilla/5.0 (Macintosh) Chrome/130.0 Safari/537.36",
			}),
		).toBe("Chrome on macOS"));
	it("handles missing device data", () =>
		expect(sessionDevice({})).toBe("Unknown device"));
	it("uses recognized structured details", () =>
		expect(sessionDevice({ browser: "Chrome", os: "Windows" })).toBe(
			"Chrome on Windows",
		));
});
