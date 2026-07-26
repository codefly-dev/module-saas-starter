// Flat config. The architecture rules below enforce the
// feature-sliced functional core pattern described in
// /FRONTEND_ARCHITECTURE.md at the module root.
//
// The rules are mechanical: you physically cannot import React from
// a core/ file, or fetch from a component, without lint failing.
// That's how we keep 80% of the logic unit-testable.

import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTypeScript from "eslint-config-next/typescript";

export default defineConfig([
	...nextVitals,
	...nextTypeScript,

	// React Compiler correctness is mandatory for application state. Third-party
	// hooks that cannot be compiled remain visible as migration warnings while
	// React safely skips those individual components.
	{
		files: ["**/*.{ts,tsx}"],
		rules: {
			"react-hooks/set-state-in-effect": "error",
			"react-hooks/incompatible-library": "warn",
		},
	},

	{
		files: ["**/*.{test,spec}.{ts,tsx}", "tests/**/*.{ts,tsx}"],
		rules: {
			"react/display-name": "off",
		},
	},
	// ------------------------------------------------------------------
	// Rule 1: src/features/*/core/** is PURE TypeScript. No React, no
	// Next, no TanStack Query, no side-effectful imports.
	// ------------------------------------------------------------------
	{
		files: ["src/features/*/core/**/*.{ts,tsx}"],
		rules: {
			"no-restricted-imports": [
				"error",
				{
					patterns: [
						{
							group: ["react", "react/*", "react-dom", "react-dom/*"],
							message:
								"core/ is pure TypeScript. Move React code to components/ or hooks/.",
						},
						{
							group: ["next", "next/*"],
							message:
								"core/ must not depend on Next.js. Use a parameter instead.",
						},
						{
							group: ["@tanstack/react-query", "@tanstack/react-query/*"],
							message:
								"core/ does not know about server state. Do the query in hooks/.",
						},
						{
							group: ["@tanstack/react-router", "@tanstack/react-router/*"],
							message: "core/ does not know about routing.",
						},
						{
							group: [
								"../api/*",
								"../hooks/*",
								"../components/*",
								"../containers/*",
							],
							message:
								"core/ is the root of the feature dependency graph — nothing can import upward from it.",
						},
					],
				},
			],
			// No global side-effects at module load time.
			"no-restricted-globals": [
				"error",
				{ name: "window", message: "core/ must not touch the DOM." },
				{ name: "document", message: "core/ must not touch the DOM." },
				{
					name: "localStorage",
					message: "core/ must not touch browser storage.",
				},
				{
					name: "sessionStorage",
					message: "core/ must not touch browser storage.",
				},
				{
					name: "fetch",
					message: "core/ must not make network calls — that's api/.",
				},
			],
		},
	},

	// ------------------------------------------------------------------
	// Rule 2: src/features/*/components/** are dumb. No data fetching,
	// no direct api/ imports. Fetching lives in hooks/.
	// ------------------------------------------------------------------
	{
		files: ["src/features/*/components/**/*.{ts,tsx}"],
		rules: {
			"no-restricted-imports": [
				"error",
				{
					patterns: [
						{
							group: ["@tanstack/react-query", "@tanstack/react-query/*"],
							message:
								"Components must not fetch. Wrap the query in a hook and pass data via props.",
						},
						{
							group: ["../api/*"],
							message:
								"Components must not call the API directly. Wire through a hook.",
						},
					],
				},
			],
		},
	},

	// ------------------------------------------------------------------
	// Rule 3: One feature MUST NOT import another feature's internals.
	// Only the public barrel (index.ts) is allowed.
	// ------------------------------------------------------------------
	{
		files: ["src/features/*/**/*.{ts,tsx}"],
		rules: {
			"no-restricted-imports": [
				"error",
				{
					patterns: [
						{
							group: [
								"@/features/*/core/*",
								"@/features/*/api/*",
								"@/features/*/hooks/*",
								"@/features/*/components/*",
								"@/features/*/containers/*",
							],
							message:
								"Cross-feature imports must go through the feature's public barrel (index.ts).",
						},
					],
				},
			],
		},
	},

	// Ignore the template directory — it's intentionally minimal scaffolding.
	globalIgnores([
		"src/features/_template/**",
		"node_modules/**",
		".next/**",
		"**/dist/**",
		"**/src/gen/**",
	]),
]);
