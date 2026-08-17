import { rmSync } from "node:fs";
import { cp, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const frontendRoot = resolve(fileURLToPath(new URL("..", import.meta.url)));
const moduleRoot = resolve(frontendRoot, "../../..");
const temporaryRoot = await mkdtemp(join(tmpdir(), "saas-published-contract-"));
process.on("exit", () =>
	rmSync(temporaryRoot, { recursive: true, force: true }),
);

function npm(commandArguments, directory) {
	execFileSync("npm", commandArguments, { cwd: directory, stdio: "inherit" });
}

function runConsumer(directory) {
	execFileSync(process.execPath, ["dist/index.js"], {
		cwd: directory,
		stdio: "inherit",
	});
}

function pack(directory) {
	const output = execFileSync(
		"npm",
		["pack", "--json", "--pack-destination", temporaryRoot],
		{ cwd: directory, encoding: "utf8" },
	);
	return join(temporaryRoot, JSON.parse(output)[0].filename);
}

const contractRoot = join(frontendRoot, "packages/saas-plugin-contract");
const reactRoot = join(frontendRoot, "packages/saas-plugin-react");
const contractArchive = pack(contractRoot);
const reactArchive = pack(reactRoot);

const referenceRoot = join(temporaryRoot, "reference");
await cp(join(moduleRoot, "contracts/reference/frontend"), referenceRoot, {
	recursive: true,
});
const referencePackagePath = join(referenceRoot, "package.json");
const referencePackage = JSON.parse(
	await readFile(referencePackagePath, "utf8"),
);
const localBuildPackage = structuredClone(referencePackage);
localBuildPackage.dependencies["@codefly/saas-plugin-contract"] =
	`file:${contractArchive}`;
localBuildPackage.dependencies["@codefly/saas-plugin-react"] =
	`file:${reactArchive}`;
await writeFile(
	referencePackagePath,
	`${JSON.stringify(localBuildPackage, null, 2)}\n`,
);
npm(["install", "--ignore-scripts", "--no-audit", "--no-fund"], referenceRoot);
npm(["run", "build"], referenceRoot);
// The package under test must carry the exact public dependency contract. The
// local tarballs above are only a build fixture; leaving file: paths in the
// packed manifest would let an unpublishable package pass this proof.
await writeFile(
	referencePackagePath,
	`${JSON.stringify(referencePackage, null, 2)}\n`,
);
const referenceArchive = pack(referenceRoot);

const consumerRoot = join(temporaryRoot, "consumer");
await cp(
	join(
		moduleRoot,
		"generated/reference-composition/services/frontend/code/src/generated",
	),
	join(consumerRoot, "src"),
	{ recursive: true },
);
await writeFile(
	join(consumerRoot, "src/index.ts"),
	`import {buildFrontendServiceAllowlist} from "@codefly/saas-plugin-contract";
import {contributedPlugins, contributedServiceBindings} from "./frontend-contributions.js";

const services = contributedPlugins.flatMap(({manifest}) =>
  (manifest.services ?? []).map((service) => ({...service, plugin: manifest.name})),
);
buildFrontendServiceAllowlist(services, contributedServiceBindings);
`,
);
await writeFile(
	join(consumerRoot, "tsconfig.json"),
	JSON.stringify(
		{
			compilerOptions: {
				target: "ES2022",
				module: "ESNext",
				moduleResolution: "Bundler",
				strict: true,
				outDir: "dist",
				skipLibCheck: true,
			},
			include: ["src/**/*.ts"],
		},
		null,
		2,
	),
);
await writeFile(
	join(consumerRoot, "package.json"),
	`${JSON.stringify(
		{
			name: "composition-contract-consumer",
			private: true,
			type: "module",
			dependencies: {
				"@codefly-reference/frontend": `file:${referenceArchive}`,
				"@codefly/saas-plugin-contract": `file:${contractArchive}`,
				"@codefly/saas-plugin-react": `file:${reactArchive}`,
				react: "19.2.8",
				typescript: "5.9.3",
			},
			scripts: { build: "tsc -p tsconfig.json" },
		},
		null,
		2,
	)}\n`,
);

npm(["install", "--ignore-scripts", "--no-audit", "--no-fund"], consumerRoot);
const installedReferencePackage = JSON.parse(
	await readFile(
		join(
			consumerRoot,
			"node_modules/@codefly-reference/frontend/package.json",
		),
		"utf8",
	),
);
for (const dependency of [
	"@codefly/saas-plugin-contract",
	"@codefly/saas-plugin-react",
]) {
	if (
		installedReferencePackage.dependencies?.[dependency] !==
		referencePackage.dependencies[dependency]
	) {
		throw new Error(
			`packed reference dependency ${dependency} is not its exact public version`,
		);
	}
}
npm(["run", "build"], consumerRoot);
runConsumer(consumerRoot);
npm(
	[
		"uninstall",
		"@codefly-reference/frontend",
		"--ignore-scripts",
		"--no-audit",
		"--no-fund",
	],
	consumerRoot,
);
await cp(
	join(
		moduleRoot,
		"services/frontend/code/src/generated/frontend-contributions.ts",
	),
	join(consumerRoot, "src/frontend-contributions.ts"),
);
npm(["run", "build"], consumerRoot);
runConsumer(consumerRoot);
npm(
	["install", referenceArchive, "--ignore-scripts", "--no-audit", "--no-fund"],
	consumerRoot,
);
await cp(
	join(
		moduleRoot,
		"generated/reference-composition/services/frontend/code/src/generated/frontend-contributions.ts",
	),
	join(consumerRoot, "src/frontend-contributions.ts"),
);
npm(["run", "build"], consumerRoot);
runConsumer(consumerRoot);
