import { execFileSync } from "node:child_process";
import { appendFileSync, existsSync, readdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
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

// Decide what to do with a version that may already be on the registry. The kit
// is a Module-Federation singleton: the bytes a solution installs must be the
// bytes the host serves. `npm pack` is deterministic, so the tarball integrity
// pins the content to the version. Three outcomes:
//   - not published            → publish
//   - published, same bytes    → skip (a release that didn't touch the kit)
//   - published, DIFFERENT bytes → fail: the kit changed without a version bump,
//     which would silently leave the registry serving stale code. Bumping the
//     version is the fix, so surface it loudly rather than skip.
export function decidePublish({
	name,
	version,
	localIntegrity,
	remoteIntegrity,
}) {
	if (remoteIntegrity === null) return "publish";
	if (remoteIntegrity === localIntegrity) return "skip";
	throw new Error(
		`${name}@${version} is already published with different contents ` +
			`(registry ${remoteIntegrity} != built ${localIntegrity}) — ` +
			`bump ${name}'s version so solutions resolve the new kit`,
	);
}

// Build the tarball and read npm's own SRI for it. `--json` writes the file and
// reports { filename, integrity } in one pass, so the integrity is the exact
// SRI of the bytes we would publish.
function pack(name, packDir) {
	const output = execFileSync(
		"npm",
		["pack", "--workspace", name, "--pack-destination", packDir, "--json"],
		{ cwd: CODE_ROOT, encoding: "utf8" },
	);
	const [entry] = JSON.parse(output);
	return { path: join(packDir, entry.filename), integrity: entry.integrity };
}

// The registry's stored SRI for an existing version, or null when the version
// was never published. Any other failure (auth, 5xx, network) rethrows: treating
// it as "not published" would blindly attempt a publish and mask the real fault.
function remoteIntegrity(name, version) {
	try {
		return execFileSync(
			"npm",
			["view", `${name}@${version}`, "dist.integrity"],
			{ cwd: CODE_ROOT, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
		).trim();
	} catch (error) {
		if (String(error.stderr ?? "").includes("E404")) return null;
		throw error;
	}
}

function main() {
	const packDir = process.env.RUNNER_TEMP ?? tmpdir();
	const manifests = workspacesByName();
	const published = [];
	for (const name of PACKAGES) {
		const manifest = manifests.get(name);
		if (!manifest) throw new Error(`no workspace package named '${name}'`);
		const { version } = manifest;
		const { path, integrity } = pack(name, packDir);
		const action = decidePublish({
			name,
			version,
			localIntegrity: integrity,
			remoteIntegrity: remoteIntegrity(name, version),
		});
		if (action === "skip") {
			console.log(`${name}@${version} already published — skipping`);
			continue;
		}
		console.log(`publishing ${name}@${version}`);
		execFileSync("npm", ["publish", path], {
			cwd: CODE_ROOT,
			stdio: "inherit",
		});
		published.push(path);
	}
	// Hand the freshly published tarballs to the workflow's attestation step.
	if (process.env.GITHUB_OUTPUT) {
		appendFileSync(
			process.env.GITHUB_OUTPUT,
			`tarballs<<EOF\n${published.join("\n")}\nEOF\n`,
		);
	}
}

if (resolve(process.argv[1] ?? "") === resolve(SCRIPT_PATH)) main();
