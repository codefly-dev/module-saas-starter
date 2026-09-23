import { notFound } from "next/navigation";

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

	return (
		<div className="flex flex-col gap-4 p-6">
			<h1 data-slot="page-title" className="type-page-title">{solution.nav.title}</h1>
			{solution.dashboard && (
				<SolutionDashboards
					graph={solution.dashboard}
					solutionId={solution.id}
				/>
			)}
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
				}}
			/>
		</div>
	);
}
