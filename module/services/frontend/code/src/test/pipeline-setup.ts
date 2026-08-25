// Global setup for the `pipeline` vitest project.
//
// The pipeline tier runs against a real Codefly dependency graph. There are two
// legitimate ways to get one, and this file makes both work without the tests
// knowing which happened:
//
//   1. `codefly test service frontend --suite integration` — Codefly starts the
//      graph and injects the dependency endpoints into this process. That is
//      the CI path, and there is nothing to do here.
//
//   2. `vitest run --project pipeline` — no Codefly wrapper, so the harness
//      starts the graph itself through the SDK's withDependencies.
//
// It runs ONCE for the whole project. Vitest isolates test files in separate
// workers, so starting the graph per file would start one graph per file.
//
// withDependencies passes --exclude-root: it brings up the frontend's
// dependencies (auth-sidecar, Accounts, Postgres, Vault, Redis, storage,
// telemetry) without the Next server the frontend itself would run. The
// pipeline tier addresses the gateway directly, so the root service is dead
// weight — and leaving it out means these tests can run alongside a
// `codefly run service` dev stack instead of fighting it for the Next dev lock.

import { type Dependencies, withDependencies } from "codefly";
import { codeflyInjectedRuntime } from "./pipeline-gateway";

// A scope keeps this graph's ports and database off the default ones, so a
// test run never collides with a running dev stack.
const DEFAULT_TEST_SCOPE = "pipeline";

export default async function setup(): Promise<(() => Promise<void>) | void> {
	if (codeflyInjectedRuntime()) {
		return;
	}

	const scope = process.env.CODEFLY_TEST_SCOPE ?? DEFAULT_TEST_SCOPE;
	// Publish the scope so the worker processes resolve endpoints in the same
	// scope this graph was started under.
	process.env.CODEFLY_TEST_SCOPE = scope;

	let dependencies: Dependencies;
	try {
		dependencies = await withDependencies({
			service: "frontend",
			// --exclude-root means the frontend is not started; probe a
			// dependency that is.
			readyService: "auth-sidecar",
			scope,
			fixture: "dev-admin",
			silents: ["store", "cache", "telemetry"],
			echo: process.env.CODEFLY_TEST_ECHO === "1",
		});
	} catch (error) {
		throw new Error(
			"pipeline setup: could not start the Codefly dependency graph. Run " +
				"`codefly test service frontend --suite integration` instead, or set " +
				"CODEFLY_TEST_ECHO=1 to see the codefly output.",
			{ cause: error },
		);
	}

	return async () => {
		// Honors CODEFLY_TEST_KEEP_ALIVE=1, which leaves the graph up so the next
		// run in this scope skips the cold start.
		await dependencies.destroy();
	};
}
