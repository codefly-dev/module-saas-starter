import { notFound } from "next/navigation";

import {
	SolutionSetupPanel,
	WhenRolesDeclared,
} from "@/features/solution-installations";
import {
	browserManifestUrl,
	findSolution,
	solutionModuleApiBase,
	solutionProxyBase,
} from "@/solutions/registry";
import { SolutionDashboards } from "@/solutions/SolutionDashboard";
import { SolutionLivePage } from "@/solutions/SolutionLivePage";
import { SolutionRuntime } from "@/solutions/SolutionRuntime";
import {
	SolutionSettings,
	SolutionWorkspace,
} from "@/solutions/SolutionWorkspace";
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

	return (
		<SolutionLivePage
			id={solution.id}
			assetVersion={solution.assetVersion}
			denied={<SolutionNotFound />}
		>
			<SolutionWorkspace
				title={solution.nav.title}
				settings={
					<WhenRolesDeclared target={solution.id} kind={solution.kind}>
						<SolutionSettings title={solution.nav.title}>
							<SolutionSetupPanel target={solution.id} />
						</SolutionSettings>
					</WhenRolesDeclared>
				}
				dashboard={
					solution.dashboard?.dashboards?.length ? (
						<SolutionDashboards
							graph={solution.dashboard}
							solutionId={solution.id}
						/>
					) : undefined
				}
			>
				{app}
			</SolutionWorkspace>
		</SolutionLivePage>
	);
}
