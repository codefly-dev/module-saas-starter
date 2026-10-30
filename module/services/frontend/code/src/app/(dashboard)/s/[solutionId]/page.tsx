import { notFound } from "next/navigation";

import { SolutionSetupPanel } from "@/features/solution-installations";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/shared/ui";
import { SolutionDashboards } from "@/solutions/SolutionDashboard";
import { SolutionLivePage } from "@/solutions/SolutionLivePage";
import { SolutionRuntime } from "@/solutions/SolutionRuntime";
import {
	browserManifestUrl,
	findSolution,
	solutionProxyBase,
	solutionModuleApiBase,
} from "@/solutions/registry";
import SolutionNotFound from "./not-found";

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
				// Same-origin base for host procedures and the solution relay. The
				// proxy routes on the path: a `saas.<pkg>.v1.<Service>/<Method>`
				// procedure reaches the API gateway's root (the host's own
				// services, e.g. the DatasourceService `<DatasourcesPanel>`
				// calls), anything else reaches this solution's upstream.
				apiBase: solutionProxyBase(solution.id),
				moduleApiBase: solutionModuleApiBase(solution.id),
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
	// while another tab is open, so switching back keeps its state (an open
	// chat, a half-typed question).
	//
	// A record whose delivery declares installation roles has a Setup tab: its
	// roles, what each grants (operations and agent tools) and who holds them —
	// the one Setup UI every host renders (`<SolutionSetup>`), answered by the
	// authority through the host installation interface. The solution is named
	// by its target, which is the route alias it is registered under.
	//
	// The record's kind is that declaration today: the shared authority core
	// composes a solution only with an installer role and refuses roles on a
	// module (codefly-dev/authority `decide.ValidateModule`), so a module page
	// has no roles to show and offers no tab that could only fail.
	const declaresRoles = solution.kind === "solution";
	return (
		<SolutionLivePage
			id={solution.id}
			assetVersion={solution.assetVersion}
			denied={<SolutionNotFound />}
		>
			<div className="flex flex-col gap-4 p-6">
				<h1 data-slot="page-title" className="type-page-title">
					{solution.nav.title}
				</h1>
				<Tabs defaultValue="app" className="w-full">
					<TabsList>
						<TabsTrigger value="app">App</TabsTrigger>
						{solution.dashboard?.dashboards?.length ? (
							<TabsTrigger value="dashboard">Dashboard</TabsTrigger>
						) : null}
						{declaresRoles ? (
							<TabsTrigger value="setup">Setup</TabsTrigger>
						) : null}
					</TabsList>
					<TabsContent value="app" keepMounted>
						{app}
					</TabsContent>
					{solution.dashboard?.dashboards?.length ? (
						<TabsContent value="dashboard">
							<SolutionDashboards
								graph={solution.dashboard}
								solutionId={solution.id}
							/>
						</TabsContent>
					) : null}
					{declaresRoles ? (
						<TabsContent value="setup">
							<SolutionSetupPanel target={solution.id} />
						</TabsContent>
					) : null}
				</Tabs>
			</div>
		</SolutionLivePage>
	);
}
