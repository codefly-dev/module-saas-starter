import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, test } from "vitest";

const featureRoot = resolve(process.cwd(), "src/features/onboarding");

function source(path: string): string {
	return readFileSync(resolve(featureRoot, path), "utf8");
}

describe("onboarding frontend architecture", () => {
	test.each([
		"application/backend.ts",
		"application/browser-draft-store.ts",
		"application/controller.ts",
		"service/client.ts",
	])("%s remains framework-free", (path) => {
		const code = source(path);
		expect(code).not.toMatch(/from ["']react/);
		expect(code).not.toMatch(/from ["']next\//);
		expect(code).not.toMatch(/@tanstack\/react-query/);
		expect(code).not.toMatch(/from ["']sonner["']/);
	});

	test("the React view cannot own backend or workflow effects", () => {
		const code = source("ui/onboarding-wizard.tsx");
		expect(code).not.toContain("@connectrpc");
		expect(code).not.toContain("@tanstack/react-query");
		expect(code).not.toMatch(/\bfetch\s*\(/);
		expect(code).not.toContain("sessionStorage");
		expect(code).not.toContain("onboardingClient");
		expect(code).not.toContain("orgMutations");
	});

	test("the React binding is the only application integration seam", () => {
		const code = source("react/use-onboarding-controller.ts");
		expect(code).toContain("new OnboardingController");
		expect(code).toContain("useSyncExternalStore");
		expect(code).not.toMatch(/\bfetch\s*\(/);
	});
});
