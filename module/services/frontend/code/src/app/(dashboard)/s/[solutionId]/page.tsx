import { notFound } from "next/navigation";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/shared/ui";
import { SolutionDashboards } from "@/solutions/SolutionDashboard";
import { SolutionRuntime } from "@/solutions/SolutionRuntime";
import {
	browserManifestUrl,
	findSolution,
	solutionProxyBase,
} from "@/solutions/registry";

export const dynamic = "force-dynamic";

export default async function SolutionPage({
	params,
}: {
	params: Promise<{ solutionId: string }>;
}) {
	const { solutionId } = await params;
	const solution = await findSolution(solutionId);
	// "unavailable" means this replica cannot read the registry, which is not
	// the same as the solution not existing. Rendering a 404 for it would tell
	// the user the page is gone when it is only unreachable.
	if (solution === "unavailable") {
		throw new Error("solution registry unavailable");
	}
	if (!solution) {
		notFound();
	}

	const app = (
		<SolutionRuntime
			remote={{
				id: solution.id,
				// A backend-relative registration is served through this host's
				// own origin; the browser is never handed a pod-local address.
				manifestUrl: browserManifestUrl(solution),
				exposedModule: solution.frontend.exposedModule,
			}}
			pageProps={{
				solutionId: solution.id,
				// Same-origin base for every backend call the remote makes. The
				// proxy routes on the path: a `saas.<pkg>.v1.<Service>/<Method>`
				// procedure reaches the API gateway's root (the host's own
				// services, e.g. the DatasourceService `<DatasourcesPanel>`
				// calls), anything else reaches this solution's upstream.
				apiBase: solutionProxyBase(solution.id),
				// What this solution declared it is built on, handed straight
				// back to it. The remote could hard-code the same repository,
				// but then two statements would have to agree: the one the host
				// validated at registration and an operator can read, and the
				// one compiled into the bundle. Returning the validated
				// declaration keeps it to one.
				...(solution.sources?.length
					? { declaredSources: solution.sources }
					: {}),
			}}
		/>
	);

	// A declared dashboard is its own tab beside the solution, not a block
	// stacked above it: stacked, it pushed the solution below the fold on every
	// visit, and a dashboard with nothing to show yet ("No data yet") sat on top
	// of the thing the person came for. The solution's panel stays mounted
	// while the dashboard is open, so switching back keeps its state (an open
	// chat, a half-typed question). A solution that declares no dashboard
	// (no graph, or a graph of events and metrics with no dashboard in it)
	// renders as before, with no tab bar for a single tab.
	return (
		<div className="flex flex-col gap-4 p-6">
			<h1 data-slot="page-title" className="type-page-title">{solution.nav.title}</h1>
			{solution.dashboard?.dashboards?.length ? (
				<Tabs defaultValue="app" className="w-full">
					<TabsList>
						<TabsTrigger value="app">App</TabsTrigger>
						<TabsTrigger value="dashboard">Dashboard</TabsTrigger>
					</TabsList>
					<TabsContent value="app" keepMounted>
						{app}
					</TabsContent>
					<TabsContent value="dashboard">
						<SolutionDashboards graph={solution.dashboard} solutionId={solution.id} />
					</TabsContent>
				</Tabs>
			) : (
				app
			)}
		</div>
	);
}
