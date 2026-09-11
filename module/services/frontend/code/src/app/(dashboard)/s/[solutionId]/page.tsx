import { notFound } from "next/navigation";

import { SolutionDashboards } from "@/solutions/SolutionDashboard";
import { SolutionRuntime } from "@/solutions/SolutionRuntime";
import { findSolution } from "@/solutions/registry";

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
			<h1 className="text-xl font-semibold">{solution.nav.title}</h1>
			{solution.dashboard && (
				<SolutionDashboards
					graph={solution.dashboard}
					solutionId={solution.id}
				/>
			)}
			<SolutionRuntime
				remote={{
					id: solution.id,
					manifestUrl: solution.frontend.manifestUrl,
					exposedModule: solution.frontend.exposedModule,
				}}
				pageProps={{
					solutionId: solution.id,
					// Same-origin gateway BFF base — the remote makes ALL backend
					// calls through here, which forwards through the API gateway.
					apiBase: `/api/solutions/${solution.id}/proxy`,
				}}
			/>
		</div>
	);
}
