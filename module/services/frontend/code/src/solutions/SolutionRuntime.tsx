"use client";

import {
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	useDashboardAuthoring,
} from "@/features/dashboard";
import {
	SolutionOutlet,
	type SolutionPageProps,
	type SolutionRemote,
} from "./SolutionOutlet";

// localStorage key the viewer's dashboard draft lives under. The authoring
// handle injected into a mounted solution commits here, so an external driver's
// edit lands in the same draft the dashboard canvas renders from — the durable,
// decoupled channel between a composing module and the live surface. The canvas
// binds the same key to reflect those edits.
export const USER_DASHBOARD_DRAFT_KEY = "dashboard:user-draft";

const EMPTY_DASHBOARD: DashboardDef = {
	version: DASHBOARD_SPEC_VERSION,
	metrics: [],
};

/**
 * Client boundary that binds the host's dashboard-authoring capability to a
 * mounted solution. It owns the viewer's draft, derives the authoring handle
 * from it, and hands that handle to the remote through SolutionOutlet. A mounted
 * module drives the live dashboard by calling the handle; the host stays
 * ignorant of how the module decides what to change.
 *
 * The draft is owned here (not inside the generic SolutionOutlet) so the outlet
 * stays pure Module Federation infra with no dependency on the app's auth,
 * audit, or query providers.
 */
export function SolutionRuntime({
	remote,
	pageProps,
}: {
	remote: SolutionRemote;
	pageProps: Omit<SolutionPageProps, "getAccessToken" | "dashboard">;
}) {
	const { authoring } = useDashboardAuthoring(
		USER_DASHBOARD_DRAFT_KEY,
		EMPTY_DASHBOARD,
	);
	return (
		<SolutionOutlet
			remote={remote}
			pageProps={pageProps}
			authoring={authoring}
		/>
	);
}
