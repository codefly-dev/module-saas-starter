// Playwright global setup: bring up the full saas-starter stack via
// the codefly JS SDK before any test runs, and shut it down on exit.
//
// Before this file existed, the stack had to be brought up manually in
// another terminal (`codefly run service frontend --fixture dev-admin`)
// and Playwright ran against whatever happened to be on port 21931.
// That was the single biggest "not-unit-test" thing about our E2E.
// Now `npx playwright test` is self-contained — identical ergonomics to
// running a Jest unit test, just with real postgres/redis/vault under
// the hood.
//
// Knobs:
//   CODEFLY_TEST_KEEP_ALIVE=1 — leave the stack running after the test
//     process exits. Subsequent `npx playwright test` runs skip the
//     40-60s cold start because the containers are already up. This is
//     the inner-loop-dev lever; flip it in your shell when iterating.
//   CODEFLY_TEST_SCOPE=<name> — use an explicit naming scope. Defaults
//     to "playwright". Different scopes get different postgres/redis
//     instances, so two terminals can run tests in parallel without
//     fighting over the same db.

import type { FullConfig } from "@playwright/test";
import {
	type Dependencies,
	resolveServiceAddressSync,
	withDependencies,
} from "codefly";

// Shared handle: globalSetup stashes it here, globalTeardown reads it.
// Playwright runs setup/teardown in the SAME Node process so a module
// variable is sufficient.
let deps: Dependencies | null = null;

async function globalSetup(_config: FullConfig): Promise<void> {
	// Default: NO scope. That keeps codefly's derived ports at the
	// deterministic defaults (frontend=21931) which matches the
	// playwright baseURL fallback. Set CODEFLY_TEST_SCOPE=<name> in the
	// shell only when you explicitly want per-process isolation, and
	// remember to pass a matching baseURL.
	const scope = process.env.CODEFLY_TEST_SCOPE ?? "";
	const keepAlive = process.env.CODEFLY_TEST_KEEP_ALIVE === "1";

	// Echo codefly's output while globalSetup is wiring things up — when
	// a test fails at "stack didn't come ready", the cause (missing
	// binary, port conflict, migration failure) shows up in the test
	// log instead of being swallowed by Playwright's reporter.
	// withDependencies passes --exclude-root, so it brings up accounts +
	// postgres/vault/redis/object-storage but NOT the frontend itself —
	// Playwright's webServer config does that in step 2. Probe the API's
	// REST endpoint (there's no FE running yet at this point). The port is
	// NOT hardcoded: `readyService: "accounts"` makes the SDK resolve the accounts service's
	// REST address from codefly (`codefly get endpoints accounts --type rest`),
	// so this works in ANY consumer workspace — the port is a workspace
	// hash and differs between, e.g., the canonical starter and warden.
	deps = await withDependencies({
		service: "frontend",
		fixture: "dev-admin",
		scope,
		silents: ["store"],
		keepAlive,
		readyTimeoutMs: 180_000,
		readyService: "accounts",
		readyPath: "/",
		echo: true,
	});

	// The FE binds to its codefly-resolved http port (NOT a hardcoded one) once
	// Playwright brings it up via webServer. Resolve the same address the
	// webServer uses so baseURL and the running server always agree.
	const frontendUrl = resolveServiceAddressSync("frontend", "http");
	if (frontendUrl) process.env.PLAYWRIGHT_BASE_URL = frontendUrl;
}

export default globalSetup;
// globalTeardown is declared in its own file per Playwright's contract;
// the pair coordinates via the module-level `deps` variable above. See
// ./global-teardown.ts for the shutdown half.
export { deps as _deps };
