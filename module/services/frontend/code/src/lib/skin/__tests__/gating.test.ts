import { describe, expect, it } from "vitest";
import { shouldResolveHost } from "../sources";

describe("shouldResolveHost", () => {
	it("reads the host at runtime when a source is configured, even without the build flag", () => {
		// The bug: the build-only flag is absent at runtime, so gating on it alone
		// left host=null and per-host skins silently collapsed to the default.
		expect(
			shouldResolveHost({
				FRONTEND_SKIN_DIR: "/etc/codefly/skin",
			} as unknown as NodeJS.ProcessEnv),
		).toBe(true);
	});

	it("reads the host at build via the flag before any source is mounted", () => {
		expect(
			shouldResolveHost({
				FRONTEND_SKIN_RUNTIME: "1",
			} as unknown as NodeJS.ProcessEnv),
		).toBe(true);
	});

	it("stays static when neither a source nor the flag is present", () => {
		expect(shouldResolveHost({} as unknown as NodeJS.ProcessEnv)).toBe(false);
	});
});
