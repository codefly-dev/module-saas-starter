import path from "path";
import { defineConfig } from "vitest/config";

export default defineConfig({
	test: {
		projects: [
			{
				extends: true,
				test: {
					name: "pure",
					globals: true,
					environment: "happy-dom",
					setupFiles: ["./src/test/setup.ts"],
					include: [
						"src/**/*.test.{ts,tsx}",
						"packages/**/*.test.{ts,tsx}",
						"scripts/**/*.test.mjs",
					],
					exclude: ["src/**/*.pipeline.test.{ts,tsx}"],
				},
			},
			{
				extends: true,
				test: {
					name: "pipeline",
					globals: true,
					environment: "node",
					include: ["src/**/*.pipeline.test.{ts,tsx}"],
					// Starts the Codefly dependency graph once for the whole
					// project when Codefly has not already provided one.
					globalSetup: ["./src/test/pipeline-setup.ts"],
					// Every pipeline file shares one Postgres. Tests namespace
					// their own rows, but serial files keep failures readable and
					// stop suites from contending over the same graph. Per-project
					// config cannot set `fileParallelism`, so run every file in one
					// fork instead — that keeps them serial without a root option.
					pool: "forks",
					poolOptions: { forks: { singleFork: true } },
					// A cold graph start plus real RPC work needs more than the
					// 5s default.
					testTimeout: 120_000,
					hookTimeout: 180_000,
				},
			},
		],
	},
	resolve: {
		alias: {
			"@": path.resolve(__dirname, "./src"),
		},
	},
});
