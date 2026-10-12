// Exercise npm version resolution without workspace links or file: dependencies.
// The local registry deliberately omits peerDependenciesMeta, like GitHub Packages.
// --registry uses the configured real registry after publication instead.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";
import { PACKAGES, workspacesByName } from "./publish-frontend-kit.mjs";

const execute = promisify(execFile);
const root = new URL("../", import.meta.url);
const temporary = await mkdtemp(join(tmpdir(), "example-registry-ui-"));
const manifests = workspacesByName(fileURLToPath(root));
const host = JSON.parse(await readFile(new URL("package.json", root), "utf8"));
const registryMode = process.argv.includes("--registry");
const archives = new Map();
let server;
let registry;
async function run(command, args, cwd = temporary) {
	try {
		const { stdout, stderr } = await execute(command, args, {
			cwd,
			maxBuffer: 8 * 1024 * 1024,
			timeout: 300_000,
		});
		if (stderr) process.stderr.write(stderr);
		return stdout;
	} catch (error) {
		if (error.stdout) process.stderr.write(error.stdout);
		if (error.stderr) process.stderr.write(error.stderr);
		throw error;
	}
}
try {
	if (!registryMode) {
		for (const name of PACKAGES) {
			const packed = JSON.parse(
				await run(
					"npm",
					[
						"pack",
						"--workspace",
						name,
						"--json",
						"--pack-destination",
						temporary,
					],
					root,
				),
			);
			archives.set(name, {
				...packed[0],
				bytes: await readFile(join(temporary, packed[0].filename)),
			});
		}
		server = createServer((request, response) => {
			const path = decodeURIComponent(
				new URL(request.url, registry).pathname,
			).slice(1);
			const archive = [...archives.values()].find(
				(entry) => path === entry.filename,
			);
			if (archive) {
				response.setHeader("content-type", "application/octet-stream");
				response.end(archive.bytes);
				return;
			}
			const packed = archives.get(path);
			const manifest = manifests.get(path);
			if (!packed || !manifest) {
				response.writeHead(404);
				response.end();
				return;
			}
			const version = { ...manifest };
			delete version.peerDependenciesMeta;
			version.dist = {
				integrity: packed.integrity,
				tarball: `${registry}${packed.filename}`,
			};
			response.setHeader("content-type", "application/json");
			response.end(
				JSON.stringify({
					name: path,
					"dist-tags": { latest: version.version },
					versions: { [version.version]: version },
				}),
			);
		});
		await new Promise((resolve, reject) => {
			server.once("error", reject);
			server.listen(0, "127.0.0.1", resolve);
		});
		registry = `http://127.0.0.1:${server.address().port}/`;
	}
	// Only consumer-facing packages are explicit: npm must discover the plugin
	// peers through version metadata, exactly as a fresh downstream install does.
	const dependencies = Object.fromEntries(
		["@codefly-dev/ui", "@codefly-dev/saas-ui", "@codefly-dev/saas-sdk"].map(
			(name) => [name, manifests.get(name).version],
		),
	);
	for (const name of [
		"react",
		"react-dom",
		"@types/react",
		"@types/react-dom",
		"typescript",
	]) {
		dependencies[name] = host.dependencies[name] ?? host.devDependencies[name];
	}
	dependencies.esbuild = JSON.parse(
		await readFile(new URL("node_modules/esbuild/package.json", root), "utf8"),
	).version;
	await writeFile(
		join(temporary, "package.json"),
		JSON.stringify({
			name: "example-registry-ui",
			private: true,
			type: "module",
			dependencies,
		}),
	);
	await writeFile(
		join(temporary, "tsconfig.json"),
		JSON.stringify({
			compilerOptions: {
				target: "ES2022",
				module: "NodeNext",
				moduleResolution: "NodeNext",
				strict: true,
				skipLibCheck: true,
			},
			include: ["consumer.ts"],
		}),
	);
	await writeFile(
		join(temporary, "consumer.ts"),
		`
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Button, Disclosure, Accordion, Breadcrumb, Tree, RadioGroup, Surface, Timeline, ViewportOverlay } from "@codefly-dev/ui/layout";
import { createTaskTracker, mountIsolated } from "@codefly-dev/ui/lifecycle";
import { resolveSkin } from "@codefly-dev/ui/skin";
import { PluginRuntimeProvider, usePluginRuntime } from "@codefly-dev/ui/plugin-host/runtime";
import { PluginRuntimeProvider as DirectProvider, usePluginRuntime as directHook } from "@codefly-dev/saas-plugin-react/runtime";
import { ConnectGitHubForm } from "@codefly-dev/saas-ui";
import { DirectoryService } from "@codefly-dev/saas-sdk";
for (const value of [Disclosure, Accordion, Breadcrumb, Tree, RadioGroup, Surface, Timeline, ViewportOverlay, createTaskTracker, mountIsolated, resolveSkin, ConnectGitHubForm, DirectoryService]) {
 if (!value) throw new Error("Missing public export");
}
if (PluginRuntimeProvider !== DirectProvider || usePluginRuntime !== directHook) throw new Error("Plugin runtime identity split");
const runtime = { service() { throw new Error("No service calls in the rendering proof"); } };
function Consumer() {
 if (directHook() !== runtime) throw new Error("Plugin context split");
 return createElement(Button, null, "Registry verified");
}
const html = renderToStaticMarkup(createElement(PluginRuntimeProvider, { runtime, children: createElement(Consumer) }));
if (!html.includes("Registry verified")) throw new Error("Published control did not render");
console.log("Registry declarations, public exports, SSR and shared plugin context passed");
`,
	);
	const options = registry ? [`--@codefly-dev:registry=${registry}`] : [];
	process.stdout.write(
		await run("npm", [
			"install",
			"--ignore-scripts",
			"--no-audit",
			"--no-fund",
			...options,
		]),
	);
	// Verify peer auto-install and ensure this proof did not pick up workspaces.
	const lock = JSON.parse(
		await readFile(join(temporary, "package-lock.json"), "utf8"),
	);
	for (const name of PACKAGES) {
		const installed = lock.packages[`node_modules/${name}`];
		assert.equal(installed?.version, manifests.get(name).version, name);
		assert.ok(!installed.link, `${name} must not be a workspace link`);
		assert.ok(
			/^https?:/.test(installed.resolved),
			`${name} must resolve from registry metadata`,
		);
	}
	await run(join(temporary, "node_modules/.bin/tsc"), ["--noEmit"]);
	await run(join(temporary, "node_modules/.bin/esbuild"), [
		"consumer.ts",
		"--bundle",
		"--platform=node",
		"--format=cjs",
		"--external:react",
		"--external:react-dom",
		"--external:react/jsx-runtime",
		"--outfile=consumer.cjs",
	]);
	process.stdout.write(await run(process.execPath, ["consumer.cjs"]));
	await writeFile(
		join(temporary, "styles.mjs"),
		`
import { readFileSync } from "node:fs";
const css = readFileSync(new URL(import.meta.resolve("@codefly-dev/ui/preview.css")), "utf8");
for (const marker of [".type-chip", "color-mix(in oklab,var(--destructive) 70%,var(--foreground))"]) {
 if (!css.includes(marker)) throw new Error("Missing compiled style: " + marker);
}
if (css.includes("@tailwind") || css.includes("@utility ")) throw new Error("Uncompiled stylesheet");
for (const name of ["theme.css", "type-slots.css"]) import.meta.resolve("@codefly-dev/ui/" + name);
console.log("Registry stylesheet exports and compiled contrast rule passed");
`,
	);
	process.stdout.write(await run(process.execPath, ["styles.mjs"]));
	console.log(
		registryMode
			? "Configured registry consumer passed"
			: "Registry consumer passed with optional-peer metadata omitted",
	);
} finally {
	if (server) {
		server.closeAllConnections();
		await new Promise((resolve) => server.close(resolve));
	}
	await rm(temporary, { recursive: true, force: true });
}
