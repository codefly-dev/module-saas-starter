import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
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
		...process.argv.slice(2),
	],
	sdk,
);
// Explicit rather than a postgenerate lifecycle hook: npm ignore-scripts must
// not leave a facade whose imported descriptors were never generated.
run(
	[
		"generate",
		"proto",
		"--proto",
		"./proto",
		"--output",
		".",
		"--local",
		"--template",
		"buf.gen.sdk.yaml",
	],
	accounts,
);

// Keep generated EOF formatting deterministic across plugin versions.
const generated = join(sdk, "generated/typescript/src/gen");
for (const path of readdirSync(generated, { recursive: true })) {
	if (!String(path).endsWith(".ts")) continue;
	const file = join(generated, String(path));
	writeFileSync(file, readFileSync(file, "utf8").trimEnd() + "\n");
}
