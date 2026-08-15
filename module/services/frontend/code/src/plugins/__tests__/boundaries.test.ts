import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, extname, join, relative } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { describe, expect, it } from "vitest";

const codeDir = join(dirname(fileURLToPath(import.meta.url)), "../../..");
const publicApi = JSON.parse(
	readFileSync(join(codeDir, "frontend-plugin-public-api.json"), "utf8"),
) as {
	packages: Record<
		string,
		{
			status: "active" | "planned";
			entrypoints: string[];
			entrypointExports: Record<string, string[]>;
		}
	>;
	forbiddenImportPatterns: string[];
	compatibilityOnlyImports: string[];
};

function sourceFiles(root: string): string[] {
	if (!existsSync(root)) return [];
	return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
		const path = join(root, entry.name);
		if (entry.isDirectory()) {
			if (["__tests__", "node_modules", ".next"].includes(entry.name))
				return [];
			return sourceFiles(path);
		}
		return [".ts", ".tsx", ".mjs"].includes(extname(entry.name)) ? [path] : [];
	});
}

function importsIn(source: string): string[] {
	return [...source.matchAll(/(?:from\s+|import\s*)["']([^"']+)["']/g)].map(
		(match) => match[1],
	);
}

function runIsolatedRuntime(
	source: string,
	{
		browser = false,
		env = {},
	}: { browser?: boolean; env?: Record<string, string> } = {},
): string {
	return execFileSync(
		process.execPath,
		[
			...(browser ? ["--conditions=browser"] : []),
			"--import",
			"tsx",
			"--input-type=module",
			"--eval",
			source,
		],
		{
			cwd: codeDir,
			encoding: "utf8",
			env: { ...process.env, ...env },
			timeout: 10_000,
		},
	).trim();
}

function publicPackageEntrypoint(
	specifier: string,
): { packageName: string; entrypoint: string } | null {
	const packageName = Object.keys(publicApi.packages).find(
		(candidate) =>
			specifier === candidate || specifier.startsWith(`${candidate}/`),
	);
	if (!packageName) return null;
	return {
		packageName,
		entrypoint:
			specifier === packageName
				? "."
				: `.${specifier.slice(packageName.length)}`,
	};
}

describe("frontend convergence boundaries", () => {
	it("keeps product branding, routes, and endpoints out of starter source", () => {
		const violations = sourceFiles(join(codeDir, "src")).flatMap((path) => {
			const source = readFileSync(path, "utf8");
			return [
				"Warden",
				"NEXT_PUBLIC_WARDEN",
				"/admin/warden",
				"/api/v1/plugins/console",
			]
				.filter((term) => source.includes(term))
				.map((term) => `${relative(codeDir, path)}: ${term}`);
		});
		expect(violations).toEqual([]);
	});

	it("forbids legacy browser service origins and direct backend URLs", () => {
		const files = [
			...sourceFiles(join(codeDir, "src")),
			join(codeDir, "next.config.mjs"),
			join(codeDir, "instrumentation-client.ts"),
		];
		const terms = [
			"NEXT_PUBLIC_API_CONNECT",
			"NEXT_PUBLIC_API_REST",
			"NEXT_PUBLIC_BACKEND_URL",
			"http://localhost:8080",
			"http://localhost:5962",
		];
		const violations = files.flatMap((path) => {
			const source = readFileSync(path, "utf8");
			return terms
				.filter((term) => source.includes(term))
				.map((term) => `${relative(codeDir, path)}: ${term}`);
		});
		expect(violations).toEqual([]);
	});

	it("leaves Node OpenTelemetry available to the designated APM owner", () => {
		const instrumentation = JSON.stringify(
			pathToFileURL(join(codeDir, "instrumentation.ts")).href,
		);
		const output = runIsolatedRuntime(
			`await import(${instrumentation}).then((module) => module.register());
const { trace } = await import("@opentelemetry/api");
const providerAccepted = trace.setGlobalTracerProvider({ getTracer() {} });
process.stdout.write(JSON.stringify({ providerAccepted }));
process.exit(0);`,
			{
				env: {
					ERROR_TRACKING_MODE: "sentry",
					NEXT_RUNTIME: "nodejs",
					SENTRY_DSN: "https://public@example.invalid/1",
				},
			},
		);

		expect(JSON.parse(output)).toEqual({ providerAccepted: true });
	});

	it("initializes browser error tracking without BrowserTracing", () => {
		const instrumentation = JSON.stringify(
			pathToFileURL(join(codeDir, "instrumentation-client.ts")).href,
		);
		const output = runIsolatedRuntime(
			`import { Window } from "happy-dom";
const browser = new Window({ url: "https://app.example" });
const globals = {
  window: browser,
  self: browser,
  document: browser.document,
  navigator: browser.navigator,
  location: browser.location,
  history: browser.history,
  XMLHttpRequest: browser.XMLHttpRequest,
  addEventListener: browser.addEventListener.bind(browser),
  removeEventListener: browser.removeEventListener.bind(browser),
  dispatchEvent: browser.dispatchEvent.bind(browser),
};
for (const [name, value] of Object.entries(globals)) {
  Object.defineProperty(globalThis, name, { configurable: true, value, writable: true });
}
await import(${instrumentation});
const Sentry = await import("@sentry/nextjs");
const client = Sentry.getClient();
const options = client?.getOptions();
process.stdout.write(JSON.stringify({
  tracesSampleRate: options?.tracesSampleRate,
  enableLogs: options?.enableLogs,
  browserTracing: client?.getIntegrationByName("BrowserTracing")?.name ?? null,
}));
await Sentry.close(0);
browser.close();
process.exit(0);`,
			{
				browser: true,
				env: {
					NEXT_PUBLIC_ERROR_TRACKING_MODE: "sentry",
					NEXT_PUBLIC_SENTRY_DSN: "https://public@example.invalid/1",
				},
			},
		);

		expect(JSON.parse(output)).toEqual({
			tracesSampleRate: 0,
			enableLogs: false,
			browserTracing: null,
		});
	});

	it("does not emit Sentry trace metadata in the resolved Next config", () => {
		const config = JSON.stringify(
			pathToFileURL(join(codeDir, "next.config.mjs")).href,
		);
		const output = runIsolatedRuntime(
			`const { default: nextConfig } = await import(${config});
process.stdout.write(JSON.stringify({
  clientTraceMetadata: nextConfig.experimental?.clientTraceMetadata ?? null,
}));`,
		);
		expect(JSON.parse(output)).toEqual({ clientTraceMetadata: null });
	});

	it("keeps Codefly endpoint resolution and plugin proxy policy server-only", () => {
		const clientFiles = sourceFiles(join(codeDir, "src")).filter((path) =>
			readFileSync(path, "utf8").startsWith('"use client"'),
		);
		const violations = clientFiles.flatMap((path) =>
			importsIn(readFileSync(path, "utf8"))
				.filter(
					(specifier) =>
						specifier === "codefly" || specifier.includes("/server/"),
				)
				.map(
					(specifier) =>
						`${relative(codeDir, path)}: server import ${specifier}`,
				),
		);
		expect(violations).toEqual([]);

		const route = readFileSync(
			join(codeDir, "src/app/api/plugins/[plugin]/[alias]/[...path]/route.ts"),
			"utf8",
		);
		expect(route).toContain("handlePluginBffRequest");
		expect(route).toContain('runtime = "nodejs"');
	});

	it("removes host compatibility shims and forbids their resurrection", () => {
		const production = sourceFiles(join(codeDir, "src"));
		const removedShims = [
			"src/lib/admin-core.ts",
			"src/lib/admin-config.ts",
			"src/lib/plugins/contracts.ts",
			"src/lib/plugins/composition.ts",
			"src/lib/hooks/use-admin-config.ts",
			"src/lib/framework/plugin.ts",
		];
		for (const shim of removedShims)
			expect(existsSync(join(codeDir, shim))).toBe(false);
		const compatibilityViolations = production.flatMap((path) => {
			const local = relative(codeDir, path);
			return importsIn(readFileSync(path, "utf8"))
				.filter((specifier) =>
					publicApi.compatibilityOnlyImports.includes(specifier),
				)
				.map((specifier) => `${local}: ${specifier}`);
		});
		expect(compatibilityViolations).toEqual([]);
		expect(
			existsSync(join(codeDir, "scripts/generate-plugin-registry.mjs")),
		).toBe(false);
		expect(existsSync(join(codeDir, "src/plugins/registry.generated.ts"))).toBe(
			false,
		);
	});

	it("enforces the generic public import map for product packages and fixtures", () => {
		const productFiles = [
			...sourceFiles(join(codeDir, "project-plugins")),
			join(codeDir, "src/test/fixtures/reference-frontend-plugin.ts"),
		];
		const forbidden = publicApi.forbiddenImportPatterns.map(
			(pattern) => new RegExp(pattern),
		);
		const violations = productFiles.flatMap((path) =>
			importsIn(readFileSync(path, "utf8")).flatMap((specifier) => {
				if (forbidden.some((pattern) => pattern.test(specifier))) {
					return [`${relative(codeDir, path)}: private import ${specifier}`];
				}
				if (!specifier.startsWith("@codefly/saas-")) return [];
				const resolved = publicPackageEntrypoint(specifier);
				if (!resolved)
					return [
						`${relative(codeDir, path)}: unknown SDK package ${specifier}`,
					];
				const declaration = publicApi.packages[resolved.packageName];
				if (declaration.status !== "active") {
					return [
						`${relative(codeDir, path)}: reserved SDK package ${specifier}`,
					];
				}
				return declaration.entrypoints.includes(resolved.entrypoint)
					? []
					: [`${relative(codeDir, path)}: private SDK entrypoint ${specifier}`];
			}),
		);
		expect(violations).toEqual([]);

		const starterPluginImports = sourceFiles(join(codeDir, "src/plugins"))
			.filter((path) => !path.includes("/__tests__/"))
			.flatMap((path) => importsIn(readFileSync(path, "utf8")));
		expect(starterPluginImports).toContain("@codefly/saas-plugin-contract");
		expect(starterPluginImports).toContain("@codefly/saas-plugin-react");

		const referenceImports = importsIn(
			readFileSync(
				join(codeDir, "src/test/fixtures/reference-frontend-plugin.ts"),
				"utf8",
			),
		);
		expect(referenceImports).toContain("@codefly/saas-plugin-contract");
		expect(referenceImports).toContain("@codefly/saas-plugin-react");
		expect(referenceImports).toContain("@codefly/saas-plugin-react/runtime");
	});

	it("keeps App Router page and layout boundaries server-first", () => {
		const violations = sourceFiles(join(codeDir, "src/app"))
			.filter((path) => /\/(?:page|layout)\.tsx$/.test(path))
			.filter((path) => readFileSync(path, "utf8").startsWith('"use client"'))
			.map((path) => relative(codeDir, path));
		expect(violations).toEqual([]);
	});

	it("mounts AdminLayout inside the two protected client shells", () => {
		const boundaries = [
			["src/app/(dashboard)/layout.tsx", "DashboardRouteShell"],
			["src/app/admin/layout.tsx", "AdminRouteShell"],
		] as const;
		for (const [path, shell] of boundaries) {
			const source = readFileSync(join(codeDir, path), "utf8");
			expect(source).toContain(shell);
			expect(source).not.toContain('"use client"');
		}
		for (const path of [
			"src/components/dashboard-route-shell.tsx",
			"src/components/admin-route-shell.tsx",
		]) {
			const source = readFileSync(join(codeDir, path), "utf8");
			expect(source).toContain(
				'import { AdminLayout } from "@/components/admin-layout"',
			);
			expect(source).toContain("<AdminLayout>");
			expect(source).not.toContain("AppShell");
		}
		expect(existsSync(join(codeDir, "src/components/app-shell.tsx"))).toBe(
			false,
		);
	});

	it("marks link-backed Base UI buttons as non-native", () => {
		const violations = sourceFiles(join(codeDir, "src")).flatMap((path) => {
			const source = readFileSync(path, "utf8");
			const linkButtons =
				source.match(
					/<Button\b(?:(?!<Button\b)[\s\S])*?render=\{<Link\b(?:(?!\/>\})[\s\S])*?\/>\}/g,
				) ?? [];
			return linkButtons
				.filter((openingTag) => !openingTag.includes("nativeButton={false}"))
				.map(
					() =>
						`${relative(codeDir, path)}: link-backed Button requires nativeButton={false}`,
				);
		});
		expect(violations).toEqual([]);
	});

	it("keeps subscription management in the authenticated admin surface", () => {
		expect(
			existsSync(join(codeDir, "src/app/(dashboard)/pricing/page.tsx")),
		).toBe(false);
		expect(
			existsSync(join(codeDir, "src/features/billing/ui/pricing-page.tsx")),
		).toBe(false);

		const binding = readFileSync(
			join(codeDir, "../frontend.bindings.codefly.yaml"),
			"utf8",
		);
		expect(binding).not.toContain("/pricing");
		expect(binding).not.toMatch(/label:\s+Pricing/);
		expect(binding).toContain("path: /admin/billing");
		expect(binding).toContain("label: Subscription");
	});

	it("supports additive product workspaces without mutating protected host inputs", () => {
		const packageJson = JSON.parse(
			readFileSync(join(codeDir, "package.json"), "utf8"),
		) as {
			workspaces?: string[];
			scripts?: Record<string, string>;
		};
		expect(packageJson.workspaces).toEqual(["packages/*"]);
		expect(packageJson.scripts?.["build:plugin-packages"]).toBe(
			"node scripts/build-plugin-workspaces.mjs",
		);
		expect(
			existsSync(join(codeDir, "scripts/build-plugin-workspaces.mjs")),
		).toBe(true);

		const nextConfig = readFileSync(join(codeDir, "next.config.mjs"), "utf8");
		expect(nextConfig).toContain("workspacePackageNames");
		expect(nextConfig).toContain('new URL("./packages"');
		expect(nextConfig).not.toContain("@warden/");

		const dockerfile = readFileSync(
			join(codeDir, "../builder/Dockerfile"),
			"utf8",
		);
		const workspaceCopy = dockerfile.indexOf("COPY code/packages ./packages");
		const cleanInstall = dockerfile.indexOf("RUN npm ci", workspaceCopy);
		expect(workspaceCopy).toBeGreaterThan(-1);
		expect(cleanInstall).toBeGreaterThan(workspaceCopy);
		expect(dockerfile).toContain(
			"COPY service.codefly.yaml /service.codefly.yaml",
		);

		const dockerignore = readFileSync(
			join(codeDir, "../.dockerignore"),
			"utf8",
		);
		expect(dockerignore).toContain("code/packages/*/node_modules");
		expect(dockerignore).toContain("code/packages/*/dist");

		const integrityPolicy = readFileSync(
			join(codeDir, "../../../tools/base-integrity.mjs"),
			"utf8",
		);
		expect(integrityPolicy).toContain(
			'rel === "services/frontend/code/package-lock.json"',
		);
		expect(integrityPolicy).toContain(
			"/^services\\/[^/]+\\/service\\.codefly\\.yaml$/.test(rel)",
		);
		expect(integrityPolicy).toContain("function workspaceInstallGraphErrors(");
		expect(integrityPolicy).toContain(
			"frontend package-lock.json workspace metadata is stale",
		);
	});
});
