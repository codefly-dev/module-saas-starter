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
		//
		// scripts/*.test.mjs (build-tooling unit tests) run here too, so the
		// default `test` suite is a single vitest invocation: the codefly nextjs
		// agent runs `npm run test -- --reporter=json --outputFile=…` and parses
		// vitest's JSON, so a second `node --test` command would swallow those
		// args and vitest would emit no report ("0 passed").
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
