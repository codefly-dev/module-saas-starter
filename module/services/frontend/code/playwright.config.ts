import { defineConfig } from "@playwright/test";
import {
	codeflyInjectedRuntime,
	productGatewayURL,
	productOrigin,
} from "./src/test/codefly-endpoints";

// Resolve EVERY address from codefly — NEVER hardcode a port. Ports are
// workspace+module+service hashes, so they differ per consumer (the canonical
// starter vs one consuming solution vs another); a hardcoded port only ever works in the one
// workspace it was authored in. The e2e requires codefly (globalSetup brings
// the stack up via withDependencies), so if resolution fails we THROW rather
// than fall back to a wrong guess.
//
// Resolution itself lives in src/test/codefly-endpoints, shared with the
// pipeline vitest tier and the e2e global setup, so the ambiguity guard and the
// scope-aware fallback cannot drift between the three harnesses — this config
// previously carried its own copy that resolved the fallback with no scope at
// all. Under `codefly test service frontend --suite e2e` Codefly injects the
// endpoints of the dependencies it started, so those win; anything it does not
// inject (the frontend's own address, while no server is running yet) resolves
// through the scoped deterministic lookup.
const frontendUrl = productOrigin(); // the FE's own codefly address
const frontendPort = new URL(frontendUrl).port;
// The browser suite exercises the product path, so its server addresses the
// same auth-gateway every other runtime does. Accounts is never a destination
// here — a direct-backend run would prove nothing about the gateway's route
// allow-list, limiter, or identity headers.
const productGateway = productGatewayURL();
// Codefly owns this process only under `codefly test service frontend --suite
// e2e`, where the graph — and possibly the frontend itself — is already up.
const codeflyOwnsProcess = codeflyInjectedRuntime();

export default defineConfig({
	testDir: "./tests/e2e",
	timeout: 45_000,
	// Two-step bring-up:
	//  1. globalSetup → withDependencies (codefly JS SDK) spawns
	//     `codefly run service frontend --exclude-root` to start the
	//     dependency graph: postgres + vault + redis + auth-gateway + accounts.
	//     (--exclude-root because the FE itself runs in step 2.)
	//  2. webServer → playwright runs a production build of the FE on the FE's
	//     codefly-resolved port. The browser talks SAME-ORIGIN to it; the Next
	//     server (src/proxy.ts) forwards /v1/* + /saas.accounts.v1.* to the
	//     auth-gateway, so auth cookies are first-party and survive full-page
	//     loads.
	//
	// Run it as `codefly test service frontend --suite e2e`. Codefly then owns
	// the process, skips step 1 (its graph is already up), and carries the
	// frontend's own Codefly configuration into the web server — which is what
	// lets the gateway verify the browser origin, so origin-derived journeys
	// (invitations, OAuth handoff) behave as they do in a real deploy. A bare
	// `npx playwright test` still traverses the same gateway, but its server
	// holds no internal-auth configuration, so the gateway forwards those
	// requests with no verified origin.
	//
	// Set CODEFLY_TEST_KEEP_ALIVE=1 in your shell to skip the codefly
	// cold start on subsequent runs — the container-level deps persist.
	globalSetup: "./tests/e2e/global-setup.ts",
	globalTeardown: "./tests/e2e/global-teardown.ts",
	webServer: {
		// Production build, not `next dev`: `next dev` compiles routes on-demand,
		// so the first hit of a heavy route (e.g. /admin) can exceed a test's
		// navigation timeout. `build && start` pre-compiles everything.
		// Port is the FE's codefly-resolved port — never hardcoded.
		command: `npm run build && npm run start -- -p ${frontendPort}`,
		url: frontendUrl,
		timeout: 300_000,
		// Reusing a leftover server silently runs a STALE build (old inlined
		// NEXT_PUBLIC_* / old port), which makes config changes appear to do
		// nothing — so a self-started run always builds fresh. Under a
		// Codefly-owned run that choice is not ours to make: if Codefly already
		// started the frontend on this port, refusing to reuse it aborts the whole
		// suite on a port collision, and there is no leftover from a previous run
		// to be stale — the server on that port is the one Codefly just started.
		reuseExistingServer: codeflyOwnsProcess,
		stdout: "pipe",
		stderr: "pipe",
		env: {
			// Fixture dev-login mode (no real WorkOS); without it the login page
			// renders no user picker and every spec times out at "Sarah Chen".
			CODEFLY__FIXTURE: "dev-admin",
			// Browser talks same-origin to the frontend; the Next server forwards
			// API traffic to the auth-gateway (src/proxy.ts). Keeps auth cookies
			// first-party so full-page loads re-auth instead of bouncing to login.
			// This server runs outside the module graph, so it needs the gateway
			// named explicitly — it is the same single product API path, not an
			// alternate one.
			PRODUCT_GATEWAY_INTERNAL: productGateway,
			// Force the Codefly fixture identity adapter for this browser suite.
			NEXT_PUBLIC_IDENTITY_PROVIDER: "fixture",
			NEXT_PUBLIC_IDENTITY_AUTHORIZE_URL: "",
			NEXT_PUBLIC_IDENTITY_CLIENT_ID: "",
			NEXT_PUBLIC_LEGAL_ENTITY_NAME: "Codefly E2E",
			NEXT_PUBLIC_LEGAL_CONTACT_EMAIL: "legal@example.test",
			NEXT_PUBLIC_LEGAL_TERMS_CONTENT:
				"E2E Terms content used only to exercise configured legal routes.",
			NEXT_PUBLIC_LEGAL_PRIVACY_CONTENT:
				"E2E Privacy content used only to exercise configured legal routes.",
		},
	},
	// Fully serial + 1 worker — tests share the same backend db + fixture seed.
	fullyParallel: false,
	workers: 1,
	retries: 1,
	use: {
		baseURL: process.env.PLAYWRIGHT_BASE_URL || frontendUrl,
		// Webhook/API-key secret journeys intentionally require the operator to
		// copy a one-shot value before acknowledging the dialog.
		permissions: ["clipboard-read", "clipboard-write"],
		screenshot: "only-on-failure",
		trace: "on-first-retry",
		actionTimeout: 10_000,
		navigationTimeout: 20_000,
	},
	projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
