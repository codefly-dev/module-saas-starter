import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
	definePlugin,
	FRONTEND_PLUGIN_CONTRACT_VERSION,
} from "@codefly/saas-plugin-contract";
import {
	defineReactFrontend,
	defineReactPlugin,
} from "@codefly/saas-plugin-react";
import { describe, expect, it } from "vitest";
import { isPermission } from "@/gen/saas/accounts/v1/frontend_catalog";
import {
	FRONTEND_NAVIGATION,
	FRONTEND_PLUGINS,
	FRONTEND_ROUTES,
} from "@/gen/saas/frontend/v1/plugin_catalog";
import frontendConfig, { installedPlugins } from "../../../frontend.config";

const codeDir = join(dirname(fileURLToPath(import.meta.url)), "../../..");

describe("explicit frontend composition root", () => {
	it("keeps built-ins first and composes every explicitly installed plugin deterministically", () => {
		const builtIns = FRONTEND_PLUGINS.map((plugin) => plugin.name);
		const installed = installedPlugins.map((plugin) => plugin.manifest.name);
		expect(installed.slice(0, builtIns.length)).toEqual(builtIns);
		expect(frontendConfig.plugins.map((plugin) => plugin.name)).toEqual(
			installed,
		);
		expect(new Set(installed).size).toBe(installed.length);
	});

	it("does not retain loose-file discovery or a generated registry", () => {
		expect(
			existsSync(join(codeDir, "scripts/generate-plugin-registry.mjs")),
		).toBe(false);
		expect(existsSync(join(codeDir, "src/plugins/registry.generated.ts"))).toBe(
			false,
		);
	});

	it("projects every built-in plugin's complete generated navigation", () => {
		for (const definition of FRONTEND_PLUGINS) {
			const plugin = frontendConfig.plugins.find(
				(candidate) => candidate.name === definition.name,
			);
			expect(plugin?.contractVersion).toBe(FRONTEND_PLUGIN_CONTRACT_VERSION);
			expect(plugin?.navItems).toEqual(
				FRONTEND_NAVIGATION.filter((item) => item.plugin === definition.name),
			);
		}
	});

	it("composes an installed product deterministically without scanning", () => {
		const productPlugin = defineReactPlugin({
			manifest: definePlugin({
				contractVersion: FRONTEND_PLUGIN_CONTRACT_VERSION,
				name: "example-product",
			}),
		});
		const configured = defineReactFrontend({
			branding: frontendConfig.branding,
			plugins: [...installedPlugins, productPlugin],
			filesystemRoutes: FRONTEND_ROUTES.map((route) => route.path),
		});
		expect(configured.plugins.map((plugin) => plugin.name)).toEqual([
			...installedPlugins.map((plugin) => plugin.manifest.name),
			"example-product",
		]);
	});

	it("pins route, navigation, and permission catalog parity", () => {
		expect(FRONTEND_ROUTES).toHaveLength(36);
		expect(FRONTEND_NAVIGATION).toHaveLength(26);
		for (const item of FRONTEND_NAVIGATION) {
			if (item.requiredPermission)
				expect(isPermission(item.requiredPermission)).toBe(true);
		}
	});
});
