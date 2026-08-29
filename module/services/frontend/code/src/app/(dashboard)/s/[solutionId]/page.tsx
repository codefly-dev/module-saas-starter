import { notFound } from "next/navigation";

import { SolutionDashboards } from "@/solutions/SolutionDashboard";
import { SolutionOutlet } from "@/solutions/SolutionOutlet";
import { findSolution } from "@/solutions/registry";

export const dynamic = "force-dynamic";

export default async function SolutionPage({
	params,
}: {
	params: Promise<{ solutionId: string }>;
}) {
	const { solutionId } = await params;
	const solution = findSolution(solutionId);
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
			<SolutionOutlet
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
