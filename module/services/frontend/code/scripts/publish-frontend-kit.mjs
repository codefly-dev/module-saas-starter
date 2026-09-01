import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SCRIPT_PATH = fileURLToPath(import.meta.url);
const CODE_ROOT = join(dirname(SCRIPT_PATH), "..");

// The solution-facing kit packages published to GitHub Packages on release.
// Kept to what a solution fe-remote genuinely consumes: `@codefly/ui` carries
// the peer-free `/layout` + `/dashboard` surface. Its plugin peers are optional
// (see the package manifest), so a solution installs those subpaths without the
// host-internal plugin packages — no need to publish them here.
export const PACKAGES = ["@codefly/ui"];

export function workspacesByName(codeRoot = CODE_ROOT) {
	const packagesRoot = join(codeRoot, "packages");
	const byName = new Map();
	for (const entry of readdirSync(packagesRoot, { withFileTypes: true })) {
		if (!entry.isDirectory()) continue;
		const manifestPath = join(packagesRoot, entry.name, "package.json");
		if (!existsSync(manifestPath)) continue;
		const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
		byName.set(manifest.name, manifest);
	}
	return byName;
}

// GitHub Packages rejects re-publishing an existing version. The kit version is
// its own semver (it bumps only when the kit's surface changes), while releases
// tag far more often — so most releases republish an unchanged version. Skip it
// rather than fail the release.
function alreadyPublished(name, version) {
	try {
		const published = execFileSync(
			"npm",
			["view", `${name}@${version}`, "version"],
			{ cwd: CODE_ROOT, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
		).trim();
		return published === version;
	} catch {
		return false;
	}
}

function main() {
	const manifests = workspacesByName();
	for (const name of PACKAGES) {
		const manifest = manifests.get(name);
		if (!manifest) throw new Error(`no workspace package named '${name}'`);
		const { version } = manifest;
		if (alreadyPublished(name, version)) {
			console.log(`${name}@${version} already published — skipping`);
			continue;
		}
		console.log(`publishing ${name}@${version}`);
		execFileSync("npm", ["publish", "--workspace", name], {
			cwd: CODE_ROOT,
			stdio: "inherit",
		});
	}
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) main();
