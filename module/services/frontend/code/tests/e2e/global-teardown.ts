// Playwright global teardown: shuts down the codefly stack that
// global-setup.ts brought up.
//
// Respects CODEFLY_TEST_KEEP_ALIVE — when set, destroy() is a no-op on
// the container-level deps so the next test run reuses them and
// skips the cold start. Single biggest lever for fast inner-loop dev.

import { _deps } from "./global-setup";

export default async function globalTeardown(): Promise<void> {
	if (!_deps) return;
	await _deps.destroy();
}
