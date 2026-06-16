import { defineConfig } from "@playwright/test";
import { resolveServiceAddressSync } from "codefly";

// Resolve the api's REST + Connect addresses from codefly at config-load
// time. These ports are deterministic codefly hashes of
// workspace+module+service, so they DIFFER per consumer (the canonical
// starter vs warden vs mind) — hardcoding them only worked in the workspace
// they were authored in. The `??` fallbacks keep things working if codefly
// can't resolve (e.g. a manually-started server with reuseExistingServer).
const apiRest =
  resolveServiceAddressSync("api", "rest") ?? "http://localhost:5962";
const apiConnect =
  resolveServiceAddressSync("api", "connect") ?? "http://localhost:44790";

export default defineConfig({
  testDir: "./tests/e2e",
  timeout: 45_000,
  // Two-step bring-up:
  //  1. globalSetup → withDependencies (codefly JS SDK) spawns
  //     `codefly run service frontend --exclude-root` to start the
  //     dependency graph: postgres + vault + redis + api + auth-sidecar.
  //     The flag is `--exclude-root` because the FE itself runs in step 2.
  //  2. webServer → playwright spawns `npm run dev -- -p 21931` so the
  //     Next.js dev server is bound to the same port playwright probes
  //     for `baseURL`. reuseExistingServer lets a manually-started FE
  //     take precedence (faster inner loop).
  //
  // Set CODEFLY_TEST_KEEP_ALIVE=1 in your shell to skip the codefly
  // cold start on subsequent runs — the container-level deps persist.
  globalSetup: "./tests/e2e/global-setup.ts",
  globalTeardown: "./tests/e2e/global-teardown.ts",
  webServer: {
    command: "npm run dev -- -p 21931",
    url: "http://localhost:21931",
    timeout: 120_000,
    reuseExistingServer: true,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      // Tells /api/fixtures and the login page they're in dev fixture
      // mode. Without this, the loader returns null and the login page
      // renders the bare "Sign in / Choose a method" placeholder with
      // no user picker — every spec then times out at "Sarah Chen".
      // codefly run injects this automatically; webServer needs it
      // explicit because it spawns `npm run dev` outside codefly.
      CODEFLY__FIXTURE: "dev-admin",
      // Connect-ES + REST endpoints the FE talks to. codefly normally
      // injects these as part of dependency wiring, but withDependencies
      // uses --exclude-root which skips ALL FE wiring, so the playwright
      // webServer spawns `next dev` with no codefly env. Without these
      // the FE falls back to localhost:8080 (Connect) and listMembers /
      // listOrganizations / etc all fail silently — empty data, blank
      // selects, every "pick an org" assertion times out.
      // Resolved from codefly above (workspace-correct), not hardcoded.
      NEXT_PUBLIC_API_CONNECT: apiConnect,
      NEXT_PUBLIC_API_REST: apiRest,
      NEXT_PUBLIC_BACKEND_URL: apiRest,
      // Force dev login flow even if the developer happens to have
      // NEXT_PUBLIC_WORKOS_* set in their shell — without this, the
      // login page treats those as configured providers and skips the
      // fixture-user list.
      NEXT_PUBLIC_WORKOS_AUTHORIZE_URL: "",
      NEXT_PUBLIC_WORKOS_CLIENT_ID: "",
      NEXT_PUBLIC_GOOGLE_AUTHORIZE_URL: "",
      NEXT_PUBLIC_GOOGLE_CLIENT_ID: "",
      NEXT_PUBLIC_AUTH0_AUTHORIZE_URL: "",
      NEXT_PUBLIC_AUTH0_CLIENT_ID: "",
    },
  },
  // Fully serial + 1 worker. Tests share the same backend database
  // (postgres / redis) and the fixture-seed path. Concurrent runs were
  // causing auth-session collisions and timeouts on cold fixture loads.
  // Serial is slower in wall time but deterministic. Flip to a multi-
  // worker config once we have per-test database isolation.
  fullyParallel: false,
  workers: 1,
  retries: 1,
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL || "http://localhost:21931",
    screenshot: "only-on-failure",
    trace: "on-first-retry",
    // Actions (clicks, fills) time out at 10s. The fixture-login flow
    // does one POST and a JWT mint — well within 10s on a warm stack.
    actionTimeout: 10_000,
    navigationTimeout: 20_000,
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
