import { act, cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	Dashboard,
	DASHBOARD_SPEC_VERSION,
	type DashboardAuthoring,
	type DashboardDef,
	scopedDashboardDraftKey,
	USER_DASHBOARD_DRAFT_KEY,
	useDashboardAuthoring,
} from "@/features/dashboard";
import { applyCommand } from "@/features/dashboard/service/dashboard-driver";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { SolutionRuntime } from "../SolutionRuntime";

const { authState } = vi.hoisted(() => ({
	authState: { organizationId: "org-1" as string | undefined },
}));
vi.mock("@/lib/auth", () => ({ useAuth: () => authState }));

// Stand in for the ./Page remote a composing module ships, capturing the handle
// the host injects so the test can drive it. The mock takes SolutionOutlet's own
// `authoring` prop, which the remote-facing `dashboardAuthoring` rename leaves
// untouched.
let injected: DashboardAuthoring | undefined;
vi.mock("../SolutionOutlet", () => ({
	SolutionOutlet: ({ authoring }: { authoring: DashboardAuthoring }) => {
		injected = authoring;
		return null;
	},
}));

const EMPTY: DashboardDef = { version: DASHBOARD_SPEC_VERSION, metrics: [] };
const REMOTE = {
	id: "s1",
	manifestUrl: "http://localhost/mf-manifest.json",
	exposedModule: "./Page",
};
const PAGE_PROPS = { solutionId: "s1", apiBase: "/api/solutions/s1/proxy" };

// The exact localStorage key both SolutionRuntime and DashboardEditor derive for
// this viewer (org-1, no signed-in user id → "anon"): if either surface stops
// deriving it through scopedDashboardDraftKey, this key stops matching and the
// channel silently breaks.
const VIEWER_DRAFT_KEY = scopedDashboardDraftKey(USER_DASHBOARD_DRAFT_KEY, {
	organizationId: "org-1",
});

// A minimal stand-in for the Dashboards canvas: any surface that binds the same
// viewer-scoped key renders the draft an external driver committed.
function CanvasStandin({ storageKey }: { storageKey: string }) {
	const { draft } = useDashboardAuthoring(storageKey, EMPTY);
	return <Dashboard data={draft.spec} />;
}

function eventTypesHandler() {
	return http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
		HttpResponse.json({
			types: [
				{
					name: "auth.login",
					version: 1,
					category: "authentication",
					owner: "accounts",
					deprecated: false,
					description: "A user logged in.",
				},
			],
		}),
	);
}

function aggregateHandler() {
	return http.post(rpc("AuditService", "AggregateAuditLog"), () =>
		HttpResponse.json({ buckets: [] }),
	);
}

beforeEach(() => {
	authState.organizationId = "org-1";
	injected = undefined;
	window.localStorage.clear();
	server.use(eventTypesHandler(), aggregateHandler());
});

afterEach(() => {
	cleanup();
	window.localStorage.clear();
});

describe("dynamic-dashboard external-driver channel", () => {
	it("injects the dashboard-authoring handle into the mounted solution runtime", () => {
		renderInApp(<SolutionRuntime remote={REMOTE} pageProps={PAGE_PROPS} />);

		// The host supplies the full authoring contract; a mounted module needs no
		// more than this handle to drive the dashboard.
		expect(injected).toBeDefined();
		expect(typeof injected?.listEventTypes).toBe("function");
		expect(typeof injected?.previewMetric).toBe("function");
		expect(typeof injected?.setDashboard).toBe("function");
	});

	it("commits an external driver's edit to the viewer-scoped draft the canvas renders", async () => {
		renderInApp(<SolutionRuntime remote={REMOTE} pageProps={PAGE_PROPS} />);

		// The composing module decides WHAT to change — here through the
		// deterministic stub driver — and drives the host solely through the
		// injected handle.
		const next = applyCommand(undefined, "logins over time").dashboard;
		let result:
			| Awaited<ReturnType<DashboardAuthoring["setDashboard"]>>
			| undefined;
		await act(async () => {
			result = await injected?.setDashboard(next);
		});
		expect(result?.ok).toBe(true);

		// The edit lands under exactly the key the Dashboards editor reads — not a
		// private key only this channel would ever see.
		const raw = window.localStorage.getItem(VIEWER_DRAFT_KEY);
		expect(raw).not.toBeNull();
		expect(
			(JSON.parse(raw as string) as DashboardDef).metrics.map((m) => m.title),
		).toContain("Logins over time");

		// A canvas bound to that key renders the committed edit: the host surface
		// reflects what the external driver changed.
		cleanup();
		renderInApp(<CanvasStandin storageKey={VIEWER_DRAFT_KEY} />);
		expect(await screen.findByText("Logins over time")).toBeTruthy();
	});

	it("returns a structured error and persists nothing for a rejected spec", async () => {
		renderInApp(<SolutionRuntime remote={REMOTE} pageProps={PAGE_PROPS} />);

		const bad: DashboardDef = {
			version: DASHBOARD_SPEC_VERSION,
			metrics: [
				{
					title: "Bad widget",
					event: { type: "auth.nope" },
					groupBy: "time",
					bucket: "day",
					chart: "line",
				},
			],
		};
		let result:
			| Awaited<ReturnType<DashboardAuthoring["setDashboard"]>>
			| undefined;
		await act(async () => {
			result = await injected?.setDashboard(bad);
		});

		expect(result?.ok).toBe(false);
		if (result?.ok !== false) return;
		expect(result.kind).toBe("validation");
		expect(result.errors[0].code).toBe("unknown_event_type");
		// A rejected spec never reaches the draft the canvas reads.
		expect(window.localStorage.getItem(VIEWER_DRAFT_KEY)).toBeNull();
	});
});
