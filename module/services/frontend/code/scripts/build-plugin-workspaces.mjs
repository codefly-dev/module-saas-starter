import { spawnSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const CODE_ROOT = join(dirname(SCRIPT_PATH), "..");
const DEPENDENCY_FIELDS = [
	"dependencies",
	"devDependencies",
	"optionalDependencies",
	"peerDependencies",
];

export function orderWorkspaceManifests(workspaces) {
	const byName = new Map();
	for (const workspace of workspaces) {
		const name = workspace.manifest?.name;
		if (typeof name !== "string" || !name) {
			throw new Error(`workspace '${workspace.directory}' has no package name`);
		}
		if (byName.has(name))
			throw new Error(`duplicate workspace package name '${name}'`);
		byName.set(name, workspace);
	}

	const localDependencies = new Map();
	const dependents = new Map([...byName].map(([name]) => [name, new Set()]));
	for (const [name, workspace] of byName) {
		const dependencies = new Set();
		for (const field of DEPENDENCY_FIELDS) {
			for (const dependency of Object.keys(workspace.manifest[field] ?? {})) {
				if (dependency === name) {
					throw new Error(`workspace package '${name}' depends on itself`);
				}
				if (byName.has(dependency)) dependencies.add(dependency);
			}
		}
		localDependencies.set(name, dependencies);
		for (const dependency of dependencies) dependents.get(dependency).add(name);
	}

	const ready = [...byName.keys()]
		.filter((name) => localDependencies.get(name).size === 0)
		.sort();
	const ordered = [];
	while (ready.length > 0) {
		const name = ready.shift();
		ordered.push(byName.get(name));
		for (const dependent of [...dependents.get(name)].sort()) {
			const dependencies = localDependencies.get(dependent);
			dependencies.delete(name);
			if (dependencies.size === 0) {
				ready.push(dependent);
				ready.sort();
			}
		}
	}
	if (ordered.length !== byName.size) {
		const cycle = [...localDependencies]
			.filter(([, dependencies]) => dependencies.size > 0)
			.map(([name]) => name)
			.sort();
		throw new Error(
			`workspace dependency graph contains a cycle: ${cycle.join(", ")}`,
		);
	}
	return ordered;
}

function readWorkspaces() {
	const packagesRoot = join(CODE_ROOT, "packages");
	if (!existsSync(packagesRoot)) return [];
	return readdirSync(packagesRoot, { withFileTypes: true })
		.filter((entry) => entry.isDirectory())
		.sort((left, right) => left.name.localeCompare(right.name))
		.flatMap((entry) => {
			const manifestPath = join(packagesRoot, entry.name, "package.json");
			if (!existsSync(manifestPath)) return [];
			return [
				{
					directory: entry.name,
					manifest: JSON.parse(readFileSync(manifestPath, "utf8")),
				},
			];
		});
}

function main() {
	for (const workspace of orderWorkspaceManifests(readWorkspaces())) {
		const result = spawnSync(
			process.platform === "win32" ? "npm.cmd" : "npm",
			["run", "build", "--workspace", workspace.manifest.name, "--if-present"],
			{ cwd: CODE_ROOT, stdio: "inherit" },
		);
		if (result.error) throw result.error;
		if (result.status !== 0) process.exit(result.status ?? 1);
	}
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) main();
