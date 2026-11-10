import { describe, expect, it } from "vitest";
import { greetingNameOf } from "./dashboard-client";

describe("greetingNameOf", () => {
	// The greeting read "Welcome Back, Maya": the whole heading capitalised and
	// the name taken from the e-mail even when the person has one.
	it("uses the person's first name when the session carries one", () => {
		expect(
			greetingNameOf({ email: "maya@acme.test", name: "Maya Rossi" }),
		).toBe("Maya");
	});

	it("falls back to the e-mail's local part, each word capitalised", () => {
		expect(greetingNameOf({ email: "jean-luc.picard@acme.test" })).toBe(
			"Jean Luc Picard",
		);
		expect(greetingNameOf(null)).toBe("there");
	});
});
