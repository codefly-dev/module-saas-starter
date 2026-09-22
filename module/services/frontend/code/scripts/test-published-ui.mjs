import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const temporary = mkdtempSync(join(tmpdir(), "example-ui-consumer-"));
const host = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
const packages = [
	"saas-plugin-contract",
	"saas-plugin-react",
	"saas-sdk",
	"codefly-ui",
	"saas-ui",
];
try {
	const dependencies = {
		esbuild: JSON.parse(
			readFileSync(join(root, "node_modules/esbuild/package.json"), "utf8"),
		).version,
	};
	for (const directory of packages) {
		const cwd = join(root, "packages", directory);
		const manifest = JSON.parse(
			readFileSync(join(cwd, "package.json"), "utf8"),
		);
		const packed = JSON.parse(
			execFileSync("npm", ["pack", "--json", "--pack-destination", temporary], {
				cwd,
				encoding: "utf8",
			}),
		);
		dependencies[manifest.name] = `file:${join(temporary, packed[0].filename)}`;
	}
	for (const name of [
		"react",
		"react-dom",
		"@types/react",
		"@types/react-dom",
		"typescript",
	]) {
		dependencies[name] = host.dependencies[name] ?? host.devDependencies[name];
	}
	writeFileSync(
		join(temporary, "package.json"),
		JSON.stringify(
			{
				name: "example-ui-consumer",
				private: true,
				type: "module",
				dependencies,
			},
			null,
			2,
		),
	);
	writeFileSync(
		join(temporary, "tsconfig.json"),
		JSON.stringify({
			compilerOptions: {
				target: "ES2022",
				module: "NodeNext",
				moduleResolution: "NodeNext",
				jsx: "react-jsx",
				strict: true,
				skipLibCheck: true,
				outDir: "dist",
			},
			include: ["consumer.tsx"],
		}),
	);
	writeFileSync(
		join(temporary, "consumer.tsx"),
		`
import { renderToStaticMarkup } from "react-dom/server";
import { CardRoot, CardContent, TabsRoot, TabsList, TabsTrigger, TabsContent, PageHeader, Button, SidebarProvider } from "@codefly-dev/ui/layout";
import { MetricCard, MetricLineChart } from "@codefly-dev/ui/dashboard";
import { DataTable } from "@codefly-dev/ui/table";
import { ConnectGitHubForm, type DatasourceClient } from "@codefly-dev/saas-ui";
const client: DatasourceClient = {listSources:async()=>[],addGitHubSource:async()=>{},syncSource:async()=>"example-job",deleteSource:async()=>{}};
const html=renderToStaticMarkup(<SidebarProvider><PageHeader title="Example workspace" /><CardRoot><CardContent><Button>Save</Button></CardContent></CardRoot><TabsRoot defaultValue="one"><TabsList><TabsTrigger value="one">Overview</TabsTrigger></TabsList><TabsContent value="one">Workspace content</TabsContent></TabsRoot><MetricCard metric={{label:"Requests",value:3}} /><MetricLineChart title="Requests" series={[]} /></SidebarProvider>);
if (!html.includes("Workspace content") || !html.includes("Save")) throw new Error("Packed UI did not render");
if (typeof DataTable !== "function" || typeof ConnectGitHubForm !== "function") throw new Error("Missing public component export");

console.log("Packed UI declarations and generic consumer rendering passed");
`,
	);
	execFileSync(
		"npm",
		["install", "--ignore-scripts", "--no-audit", "--no-fund"],
		{ cwd: temporary, stdio: "inherit" },
	);
	execFileSync("npx", ["tsc", "--noEmit"], {
		cwd: temporary,
		stdio: "inherit",
	});
	execFileSync(
		"npx",
		[
			"esbuild",
			"consumer.tsx",
			"--bundle",
			"--platform=node",
			"--format=cjs",
			"--external:react",
			"--external:react-dom",
			"--external:react/jsx-runtime",
			"--outfile=consumer.cjs",
		],
		{ cwd: temporary, stdio: "inherit" },
	);
	execFileSync(process.execPath, ["consumer.cjs"], {
		cwd: temporary,
		stdio: "inherit",
	});
} finally {
	rmSync(temporary, { recursive: true, force: true });
}
