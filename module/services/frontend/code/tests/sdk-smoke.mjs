// Smoke test with echo: true so we can see what codefly is actually
// doing. Short timeout (45s) so iteration is fast.

import { withDependencies } from "codefly";

const start = Date.now();
console.log("[smoke] spawning codefly...");

try {
	const deps = await withDependencies({
		service: "frontend",
		fixture: "dev-admin",
		silents: ["store"],
		readyTimeoutMs: 240_000,
		readyPath: "/auth/login",
		echo: true,
	});
	const ms = Date.now() - start;
	console.log(`[smoke] READY in ${ms}ms at ${deps.baseURL} (pid ${deps.pid})`);

	const res = await fetch(deps.baseURL + "/auth/login");
	console.log(`[smoke] GET ${deps.baseURL}/auth/login → ${res.status}`);

	await deps.destroy();
	console.log("[smoke] destroyed.");
	process.exit(0);
} catch (err) {
	console.error("[smoke] FAIL:", err instanceof Error ? err.message : err);
	process.exit(1);
}
