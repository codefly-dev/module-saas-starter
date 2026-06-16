import { defineConfig } from "@playwright/test";
import { resolveServiceAddressSync } from "codefly";

// Resolve EVERY address from codefly — NEVER hardcode a port. Ports are
// workspace+module+service hashes, so they differ per consumer (the canonical
// starter vs warden vs mind); a hardcoded port only ever works in the one
// workspace it was authored in. The e2e requires codefly (globalSetup brings
// the stack up via withDependencies), so if resolution fails we THROW rather
// than fall back to a wrong guess.
function mustResolve(service: string, type: "http" | "rest" | "connect"): string {
  const url = resolveServiceAddressSync(service, type);
  if (!url) {
    throw new Error(
      `playwright.config: codefly could not resolve ${service}/${type}. ` +
        `Run inside the workspace with codefly on PATH — the e2e depends on it.`,
    );
  }
  return url;
}

const frontendUrl = mustResolve("frontend", "http"); // the FE's own codefly address
const frontendPort = new URL(frontendUrl).port;
const apiRest = mustResolve("api", "rest"); // rewrite destination (server-side)
const apiConnect = mustResolve("api", "connect"); // rewrite destination (server-side)

export default defineConfig({
  testDir: "./tests/e2e",
  timeout: 45_000,
  // Two-step bring-up:
  //  1. globalSetup → withDependencies (codefly JS SDK) spawns
  //     `codefly run service frontend --exclude-root` to start the
  //     dependency graph: postgres + vault + redis + api. (--exclude-root
  //     because the FE itself runs in step 2.)
  //  2. webServer → playwright runs a production build of the FE on the FE's
  //     codefly-resolved port. The browser talks SAME-ORIGIN to it; the Next
  //     server proxies /v1/* + /customers.* to the api (next.config rewrites),
  //     so auth cookies are first-party and survive full-page loads.
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
    // Always start a fresh server: reusing a leftover one silently runs a
    // STALE build (old inlined NEXT_PUBLIC_* / old port), which makes config
    // changes appear to do nothing. Determinism over inner-loop speed.
    reuseExistingServer: false,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      // Fixture dev-login mode (no real WorkOS); without it the login page
      // renders no user picker and every spec times out at "Sarah Chen".
      CODEFLY__FIXTURE: "dev-admin",
      // Browser talks SAME-ORIGIN to the FE (its resolved URL); the Next
      // server proxies API traffic to the api (next.config rewrites). Keeps
      // auth cookies first-party so full-page loads re-auth instead of
      // bouncing to login.
      NEXT_PUBLIC_API_CONNECT: frontendUrl,
      NEXT_PUBLIC_API_REST: frontendUrl,
      NEXT_PUBLIC_BACKEND_URL: frontendUrl,
      // Rewrite destinations — the api's real, codefly-resolved ports.
      API_REST_INTERNAL: apiRest,
      API_CONNECT_INTERNAL: apiConnect,
      // Force the fixture login flow even if the dev has NEXT_PUBLIC_WORKOS_*
      // set in their shell.
      NEXT_PUBLIC_WORKOS_AUTHORIZE_URL: "",
      NEXT_PUBLIC_WORKOS_CLIENT_ID: "",
      NEXT_PUBLIC_GOOGLE_AUTHORIZE_URL: "",
      NEXT_PUBLIC_GOOGLE_CLIENT_ID: "",
      NEXT_PUBLIC_AUTH0_AUTHORIZE_URL: "",
      NEXT_PUBLIC_AUTH0_CLIENT_ID: "",
    },
  },
  // Fully serial + 1 worker — tests share the same backend db + fixture seed.
  fullyParallel: false,
  workers: 1,
  retries: 1,
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL || frontendUrl,
    screenshot: "only-on-failure",
    trace: "on-first-retry",
    actionTimeout: 10_000,
    navigationTimeout: 20_000,
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
