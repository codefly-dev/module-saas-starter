import { act, cleanup, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	DASHBOARD_SPEC_VERSION,
	Dashboard,
	type DashboardAuthoring,
	type DashboardDef,
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
// the host injects so the wiring — not just the contract — is exercised.
let injected: DashboardAuthoring | undefined;
vi.mock("../SolutionOutlet", () => ({
	SolutionOutlet: ({ authoring }: { authoring: DashboardAuthoring }) => {
		injected = authoring;
		return null;
	},
}));

const EMPTY: DashboardDef = { version: DASHBOARD_SPEC_VERSION, metrics: [] };

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

// A canvas bound to the same draft the injected handle commits to — exactly what
// the host renders. `capture` hands the test the live handle so it can drive
// edits the way a mounted module would.
function Canvas({
	capture,
}: {
	capture: (handle: DashboardAuthoring) => void;
}) {
	const { authoring, draft } = useDashboardAuthoring("dashboard:test", EMPTY);
	capture(authoring);
	return <Dashboard data={draft.spec} />;
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
		renderInApp(
			<SolutionRuntime
				remote={{
					id: "s1",
					manifestUrl: "http://localhost/mf-manifest.json",
					exposedModule: "./Page",
				}}
				pageProps={{ solutionId: "s1", apiBase: "/api/solutions/s1/proxy" }}
			/>,
		);

		// The host supplies the full authoring contract; a mounted module needs no
		// more than this handle to drive the dashboard.
		expect(injected).toBeDefined();
		expect(typeof injected?.listEventTypes).toBe("function");
		expect(typeof injected?.previewMetric).toBe("function");
		expect(typeof injected?.setDashboard).toBe("function");
	});

	it("reflects an external driver's edit on the live host canvas", async () => {
		let handle: DashboardAuthoring | undefined;
		renderInApp(<Canvas capture={(h) => (handle = h)} />);
		expect(screen.queryByText("Logins over time")).toBeNull();

		// The composing module decides WHAT to change — here through the
		// deterministic stub driver — and drives the host solely through the
		// injected handle.
		const next = applyCommand(undefined, "logins over time").dashboard;
		let result:
			| Awaited<ReturnType<DashboardAuthoring["setDashboard"]>>
			| undefined;
		await act(async () => {
			result = await handle?.setDashboard(next);
		});

		expect(result?.ok).toBe(true);
		expect(await screen.findByText("Logins over time")).toBeTruthy();
	});

	it("returns a structured error a driver can correct for a rejected spec", async () => {
		let handle: DashboardAuthoring | undefined;
		renderInApp(<Canvas capture={(h) => (handle = h)} />);

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
			result = await handle?.setDashboard(bad);
		});

		expect(result?.ok).toBe(false);
		if (result?.ok !== false) return;
		expect(result.kind).toBe("validation");
		expect(result.errors[0].code).toBe("unknown_event_type");
		// The rejected metric never reached the canvas.
		expect(screen.queryByText("Bad widget")).toBeNull();
	});
});
