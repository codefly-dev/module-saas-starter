import { readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve, sep } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const sdk = fileURLToPath(new URL("../", import.meta.url));
const accounts = fileURLToPath(
	new URL("../../../../../accounts/", import.meta.url),
);
function run(args, cwd) {
	const result = spawnSync("codefly", args, { cwd, stdio: "inherit" });
	if (result.error) throw result.error;
	if (result.status !== 0) process.exit(result.status ?? 1);
}
const args = process.argv.slice(2);
// Refreshing the bindings alone leaves the vendored contract and facade — and
// the digest they record — exactly as they are.
const bindingsOnly = args.includes("--bindings-only");
// The selector is this script's own; the CLI has no such option, so no spelling
// of it may reach the step that forwards its arguments.
const forwarded = args.filter((arg) => !arg.startsWith("--bindings-only"));
if (!bindingsOnly) {
	run(
		[
			"generate",
			"client",
			"--from",
			"contracts:../../../../../contracts/api",
			"--endpoint",
			"accounts/connect",
			"--language",
			"typescript",
			"--services",
			"AccessibleScopeService,AuditService,DatasourceService,WebhookService",
			"--name",
			"saas-sdk",
			"--npm-scope",
			"@codefly-dev/saas-sdk",
			"--module-name",
			"accounts",
			"--output",
			"./generated",
			...forwarded,
		],
		sdk,
	);
}
// Explicit rather than a postgenerate lifecycle hook: npm ignore-scripts must
// not leave a facade whose imported descriptors were never generated. Both steps
// read the exported snapshot, never the mutable service proto source.
run(
	[
		"generate",
		"proto",
		"--proto",
		"../../contracts/api/accounts/connect/proto",
		"--output",
		".",
		"--local",
		"--template",
		"buf.gen.sdk.yaml",
	],
	accounts,
);

const generated = join(sdk, "generated/typescript/src/gen");
// The facade sits beside `gen/` and imports into it, so the import graph is the
// whole generated source tree. Deciding "nothing imports this" from `gen/` alone
// would call a descriptor reached only from the facade stale, and delete a module
// the package's own entry point imports.
const generatedSource = join(sdk, "generated/typescript/src");
const listTypeScript = (root) =>
	readdirSync(root, { recursive: true })
		.map(String)
		.filter((path) => path.endsWith(".ts"));

// `buf.gen.sdk.yaml` declares no `clean`, so the backstop overwrites what it
// re-emits and leaves everything else exactly where it is. The pinned plugin
// resolves every well-known type to `@bufbuild/protobuf/wkt` and emits no file
// for it, so the client step's own `google/protobuf` descriptors outlive the
// run — present, at whichever version wrote them, and imported by nothing.
// Dropping them is what makes this command reproduce the committed tree. To a
// fixed point: removing one can orphan another that only it imported.
//
// Only that tree may be pruned. Every other proto the vendored contract names is
// REQUIRED on disk by assertLibraryBindingsMatchContract (module/tools/
// composition), which skips exactly this prefix — so deleting a `buf/validate` or
// `google/api` binding would leave no tree that satisfies both gates.
const wellKnownTypeRoot = "google/protobuf/";
const importTarget = (from, specifier) => {
	const target = resolve(dirname(join(generatedSource, from)), specifier);
	if (target.endsWith(".js")) return `${target.slice(0, -3)}.ts`;
	return target.endsWith(".ts") ? target : `${target}.ts`;
};
for (let pruning = true; pruning; ) {
	pruning = false;
	const imported = new Set();
	for (const path of listTypeScript(generatedSource)) {
		const source = readFileSync(join(generatedSource, path), "utf8");
		// Both quote styles: a specifier this misses is a dependency believed to be
		// unreferenced, and then deleted.
		for (const [, specifier] of source.matchAll(/from\s*["']([^"']+)["']/g)) {
			if (specifier.startsWith(".")) imported.add(importTarget(path, specifier));
		}
	}
	for (const path of listTypeScript(generated)) {
		if (!path.split(sep).join("/").startsWith(wellKnownTypeRoot)) continue;
		if (imported.has(join(generated, path))) continue;
		rmSync(join(generated, path));
		pruning = true;
	}
}

// Keep generated EOF formatting deterministic across plugin versions.
for (const path of listTypeScript(generated)) {
	const file = join(generated, path);
	writeFileSync(file, readFileSync(file, "utf8").trimEnd() + "\n");
}
