import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
	buildFrontendServiceAllowlist,
	type FrontendServiceBinding,
} from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";
import frontendConfig, { serviceBindings } from "../../../frontend.config";

const codeDir = join(dirname(fileURLToPath(import.meta.url)), "../../..");

describe("generated plugin service allowlist", () => {
	it("matches the application composition exactly", () => {
		const generated = JSON.parse(
			readFileSync(
				join(codeDir, "server/plugin-service-allowlist.generated.json"),
				"utf8",
			),
		);
		expect(generated).toEqual(
			JSON.parse(
				JSON.stringify(
					buildFrontendServiceAllowlist(
						frontendConfig.services,
						serviceBindings,
					),
				),
			),
		);
	});

	it("keeps application bindings logical and complete for canonical or consumer composition", () => {
		expect(serviceBindings).toHaveLength(frontendConfig.services.length);
		const bindings: readonly FrontendServiceBinding[] = serviceBindings;
		for (const binding of bindings) {
			expect(Object.keys(binding).sort()).toEqual([
				"alias",
				"plugin",
				"target",
			]);
			expect(Object.keys(binding.target).sort()).toEqual(["module", "service"]);
			expect(JSON.stringify(binding)).not.toMatch(
				/https?:|credential|password|token|url/i,
			);
		}
	});
});
