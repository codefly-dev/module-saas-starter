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
const bindings = () =>
	readdirSync(generated, { recursive: true })
		.map(String)
		.filter((path) => path.endsWith(".ts"));

// `buf.gen.sdk.yaml` declares no `clean`, so the backstop overwrites what it
// re-emits and leaves everything else exactly where it is. The pinned plugin
// resolves every well-known type to `@bufbuild/protobuf/wkt` and emits no file
// for it, so the client step's own `google/protobuf` descriptors outlive the
// run — present, at whichever version wrote them, and imported by nothing.
// Dropping them is what makes this command reproduce the committed tree. To a
// fixed point: removing one can orphan another that only it imported.
const thirdPartyRoots = ["buf", "google"];
const importTarget = (from, specifier) => {
	const target = resolve(dirname(join(generated, from)), specifier);
	if (target.endsWith(".js")) return `${target.slice(0, -3)}.ts`;
	return target.endsWith(".ts") ? target : `${target}.ts`;
};
for (let pruning = true; pruning; ) {
	pruning = false;
	const present = bindings();
	const imported = new Set();
	for (const path of present) {
		const source = readFileSync(join(generated, path), "utf8");
		for (const [, specifier] of source.matchAll(/from\s*"(\.[^"]*)"/g)) {
			imported.add(importTarget(path, specifier));
		}
	}
	for (const path of present) {
		if (!thirdPartyRoots.includes(path.split(sep)[0])) continue;
		if (imported.has(join(generated, path))) continue;
		rmSync(join(generated, path));
		pruning = true;
	}
}

// Keep generated EOF formatting deterministic across plugin versions.
for (const path of bindings()) {
	const file = join(generated, path);
	writeFileSync(file, readFileSync(file, "utf8").trimEnd() + "\n");
}
