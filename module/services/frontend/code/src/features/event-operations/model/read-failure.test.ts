import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { readFailureWords } from "./read-failure";

describe("readFailureWords", () => {
	it("says who can grant the role when the read was refused, and offers no retry", () => {
		const words = readFailureWords(
			"the event types",
			new ConnectError("platform:jobs:read required", Code.PermissionDenied),
		);
		expect(words.title).toBe("You can't see the event types");
		expect(words.detail).toMatch(/Ask a platform super administrator/);
		expect(words.detail).not.toMatch(/try again|retry/i);
		expect(words.canRetry).toBe(false);
	});

	it("offers a retry for a real failure and never the error's own text", () => {
		const words = readFailureWords(
			"the event types",
			new ConnectError("dial tcp 10.0.0.1: i/o timeout", Code.Unavailable),
		);
		expect(words.title).toBe("Couldn't load the event types");
		expect(words.canRetry).toBe(true);
		expect(`${words.title} ${words.detail}`).not.toMatch(/tcp|timeout/);
	});
});
