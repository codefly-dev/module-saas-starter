import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { buildFrontendServiceAllowlist } from "@codefly/saas-plugin-contract";
import frontendConfig, { serviceBindings } from "../frontend.config";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const outputPath = join(
	scriptDir,
	"../server/plugin-service-allowlist.generated.json",
);
const serialized = `${JSON.stringify(
	buildFrontendServiceAllowlist(frontendConfig.services, serviceBindings),
	null,
	2,
)}\n`;

if (process.argv.includes("--check")) {
	if (
		!existsSync(outputPath) ||
		readFileSync(outputPath, "utf8") !== serialized
	) {
		console.error(
			"plugin service allowlist is stale; run npm run generate:plugin-allowlist",
		);
		process.exitCode = 1;
	}
} else {
	mkdirSync(dirname(outputPath), { recursive: true });
	writeFileSync(outputPath, serialized);
}
