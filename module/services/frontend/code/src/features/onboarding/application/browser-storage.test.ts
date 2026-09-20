import { describe, expect, it } from "vitest";

import { browserStorage } from "./browser-storage";

describe("browserStorage never lets the storage getter throw", () => {
	it("returns null with no window at all (server render)", () => {
		expect(browserStorage("local", null)).toBe(null);
	});

	it("returns null when the getter itself throws", () => {
		const scope = {
			get localStorage(): Storage {
				throw new DOMException("blocked", "SecurityError");
			},
			get sessionStorage(): Storage {
				throw new DOMException("blocked", "SecurityError");
			},
		};
		expect(browserStorage("local", scope)).toBe(null);
		expect(browserStorage("session", scope)).toBe(null);
	});

	it("returns the storage the kind names when it is readable", () => {
		const local = { getItem: () => null } as unknown as Storage;
		const session = { getItem: () => null } as unknown as Storage;
		const scope = { localStorage: local, sessionStorage: session };
		expect(browserStorage("local", scope)).toBe(local);
		expect(browserStorage("session", scope)).toBe(session);
	});
});
