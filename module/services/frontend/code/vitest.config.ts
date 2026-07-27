import path from "path";
import { defineConfig } from "vitest/config";

export default defineConfig({
	test: {
		globals: true,
		environment: "happy-dom",
		setupFiles: ["./src/test/setup.ts"],
		// Unit tests only. Playwright e2e specs (tests/e2e/*.spec.ts) run via
		// `npm run test:e2e`, not vitest — without this, vitest tries to collect
		// them and fails on the @playwright/test import.
		include: [
			"src/**/*.test.{ts,tsx}",
			"packages/**/*.test.{ts,tsx}",
			"scripts/**/*.test.mjs",
		],
	},
	resolve: {
		alias: {
			"@": path.resolve(__dirname, "./src"),
		},
	},
});
