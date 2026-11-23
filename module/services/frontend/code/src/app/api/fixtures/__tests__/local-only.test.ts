// The real route over the real loader and the real fixture files: nothing is
// mocked, because the property under test is the gate itself. A fixture file
// carries every fixture person's sign-in token, and this route answers an
// unauthenticated caller.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

import { GET } from "@/app/api/fixtures/route";
import { isLocalEnvironment } from "@/lib/fixtures/loader";

function selectFixture(environment: string | undefined) {
	vi.stubEnv("CODEFLY__FIXTURE", "dev-admin");
	if (environment === undefined) {
		vi.stubEnv("CODEFLY__ENVIRONMENT", undefined);
	} else {
		vi.stubEnv("CODEFLY__ENVIRONMENT", environment);
	}
}

afterEach(() => {
	vi.unstubAllEnvs();
});

describe("fixtures route is served by a local fixture run only", () => {
	it("serves the selected fixture, tokens included, in the local environment", async () => {
		selectFixture("local");

		const res = GET();

		expect(res.status).toBe(200);
		const body = (await res.json()) as {
			name: string;
			users: { provider_id: string }[];
		};
		expect(body.name).toBe("dev-admin");
		expect(body.users.length).toBeGreaterThan(0);
		expect(body.users[0].provider_id).toBeTruthy();
	});

	it.each([
		["absent", undefined],
		["empty", ""],
		["production", "production"],
		["staging", "staging"],
		["an unknown name", "preview-7"],
		["a name that only contains local", "local-k8s"],
		["a name that only starts like local", "localhost"],
	])(
		"404s with a fixture selected when the environment is %s",
		async (_label, environment) => {
			selectFixture(environment);

			const res = GET();

			expect(res.status).toBe(404);
			const text = JSON.stringify(await res.json());
			expect(text).not.toContain("provider_id");
			expect(text).not.toContain("fixture_token");
		},
	);

	it("does not take the browser-facing copy of the environment as the process's own", async () => {
		selectFixture(undefined);
		vi.stubEnv("NEXT_PUBLIC_CODEFLY__ENVIRONMENT", "local");

		expect(GET().status).toBe(404);
	});

	it("404s in the local environment when no fixture is selected", () => {
		vi.stubEnv("CODEFLY__ENVIRONMENT", "local");
		vi.stubEnv("CODEFLY__FIXTURE", "");

		expect(GET().status).toBe(404);
	});

	it("reads the environment name the way the Go SDK does", () => {
		expect(isLocalEnvironment({ CODEFLY__ENVIRONMENT: "local" })).toBe(true);
		expect(isLocalEnvironment({ CODEFLY__ENVIRONMENT: "LOCAL" })).toBe(true);
		expect(isLocalEnvironment({ CODEFLY__ENVIRONMENT: "" })).toBe(false);
		expect(isLocalEnvironment({})).toBe(false);
		expect(isLocalEnvironment({ CODEFLY__ENVIRONMENT: "production" })).toBe(
			false,
		);
	});
});
