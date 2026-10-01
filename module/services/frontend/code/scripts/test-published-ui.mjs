import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
import { CardRoot, CardContent, TabsRoot, TabsList, TabsTrigger, TabsContent, PageHeader, Button, SidebarProvider, ViewportOverlay } from "@codefly-dev/ui/layout";
import { MetricCard, MetricLineChart } from "@codefly-dev/ui/dashboard";
import { DataTable } from "@codefly-dev/ui/table";
import { Content } from "@codefly-dev/ui/content";
import { createTaskTracker, mountIsolated } from "@codefly-dev/ui/lifecycle";
import { ConnectGitHubForm, type DatasourceClient } from "@codefly-dev/saas-ui";
const client: DatasourceClient = {listSources:async()=>[],addGitHubSource:async()=>{},syncSource:async()=>"example-job",deleteSource:async()=>{}};
const html=renderToStaticMarkup(<SidebarProvider><PageHeader title="Example workspace" /><CardRoot><CardContent><Button>Save</Button></CardContent></CardRoot><TabsRoot defaultValue="one"><TabsList><TabsTrigger value="one">Overview</TabsTrigger></TabsList><TabsContent value="one">Workspace content</TabsContent></TabsRoot><MetricCard metric={{label:"Requests",value:3}} /><MetricLineChart title="Requests" series={[]} /><Content value={"**Rendered** answer"} /></SidebarProvider>);
if (!html.includes("Workspace content") || !html.includes("Save") || !html.includes("<strong>Rendered</strong>")) throw new Error("Packed UI did not render");
if (typeof DataTable !== "function" || typeof ConnectGitHubForm !== "function") throw new Error("Missing public component export");

const tasks = createTaskTracker<{ value: string }, { output: string }>();
const task = tasks.begin({ value: "captured" });
tasks.invalidate();
tasks.settle(task, { output: "retained" });
const entry = tasks.entries({ limit: 1 })[0];
if (tasks.isCurrent(task) || entry?.state !== "settled" || entry.outcome.output !== "retained" || typeof mountIsolated !== "function") throw new Error("Packed lifecycle did not retain its captured result");
const overlay = renderToStaticMarkup(<ViewportOverlay items={[{id:"mark",regions:[{kind:"rect",x:1,y:2,width:3,height:4},{kind:"polygon",points:[[5,6],[7,8],[9,10]]}],stroke:"color-mix(in srgb, red 50%, blue)",fill:"transparent",attributes:{"data-mark":"packed"}}]} />);
if (!overlay.includes('data-mark="packed"') || !overlay.includes('points="5,6 7,8 9,10"') || !overlay.includes('stroke="color-mix(in srgb, red 50%, blue)"')) throw new Error("Packed overlay lost geometry or styling");
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
