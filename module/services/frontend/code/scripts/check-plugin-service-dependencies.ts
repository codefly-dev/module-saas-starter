import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import type { FrontendServiceAllowlist } from "@codefly/saas-plugin-contract";

import { assertPluginServiceDependenciesCurrent } from "../server/plugin-service-dependency-policy";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const allowlist = JSON.parse(
	readFileSync(
		join(scriptDir, "../server/plugin-service-allowlist.generated.json"),
		"utf8",
	),
) as FrontendServiceAllowlist;
const serviceManifest = readFileSync(
	join(scriptDir, "../../service.codefly.yaml"),
	"utf8",
);

try {
	assertPluginServiceDependenciesCurrent(allowlist, serviceManifest);
} catch (error) {
	console.error(error instanceof Error ? error.message : String(error));
	process.exitCode = 1;
}
