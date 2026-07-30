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
