#!/usr/bin/env node
// Emit the two published dashboard-vocabulary documents from the kit's own types.
//
// `src/dashboard/catalog.ts` is the source: the A2UI catalog and the data-model
// schema, type-checked against `DashboardView` and `WidgetVisualization` so the
// kit cannot publish a vocabulary it does not have. These files are the *bytes*
// of that source, committed because a consumer freezes a catalog by the digest of
// the document it was approved under — a consumer serialising the object itself
// would digest its own formatting.
//
//   node scripts/generate-dashboard-catalog.mjs           # write (from packages/codefly-ui)
//   node scripts/generate-dashboard-catalog.mjs --check    # fail if stale
//
// The `--check` form is what a developer runs; `src/dashboard/__tests__/catalog.test.ts`
// makes the same comparison inside the suite, so a hand-edited document fails CI
// whether or not this script is wired into a job.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const PACKAGE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// Imported as TypeScript: node strips the types, and `catalog.ts` imports only
// types from its siblings, so nothing else has to be built first.
const { DASHBOARD_CATALOG, DASHBOARD_DATA_SCHEMA } = await import(
	join(PACKAGE_ROOT, "src/dashboard/catalog.ts")
);

// Tabs and a trailing newline: the repository's other generated JSON is
// formatted this way, so the committed bytes survive a formatter run.
const serialize = (document) => `${JSON.stringify(document, null, "\t")}\n`;

const OUTPUTS = [
	{
		path: join(PACKAGE_ROOT, "src/dashboard/dashboard.catalog.generated.json"),
		rendered: serialize(DASHBOARD_CATALOG),
	},
	{
		path: join(
			PACKAGE_ROOT,
			"src/dashboard/dashboard-data.schema.generated.json",
		),
		rendered: serialize(DASHBOARD_DATA_SCHEMA),
	},
];

if (process.argv.includes("--check")) {
	let stale = false;
	for (const { path, rendered } of OUTPUTS) {
		let current = "";
		try {
			current = readFileSync(path, "utf8");
		} catch {
			console.error(`dashboard catalog: ${path} is missing`);
			stale = true;
			continue;
		}
		if (current !== rendered) {
			console.error(`dashboard catalog: ${path} is stale`);
			stale = true;
		}
	}
	if (stale) {
		console.error(
			"run node scripts/generate-dashboard-catalog.mjs from packages/codefly-ui",
		);
		process.exit(1);
	}
	console.log(
		`dashboard catalog OK: ${Object.keys(DASHBOARD_CATALOG.components).length} components`,
	);
} else {
	for (const { path, rendered } of OUTPUTS) writeFileSync(path, rendered);
	console.log(
		`dashboard catalog written: ${Object.keys(DASHBOARD_CATALOG.components).length} components`,
	);
}
