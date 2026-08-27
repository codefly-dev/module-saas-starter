import {
	createPluginRuntime,
	PluginAvailabilityError,
	PluginRuntimeProvider,
} from "@codefly/ui/plugin-host/runtime";
import { act, cleanup, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PluginContributionBoundary } from "@/components/plugin-contribution-boundary";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

function silenceReactError(): void {
	vi.spyOn(console, "error").mockImplementation(() => undefined);
}

describe("host plugin contribution isolation", () => {
	it.each([
		["unavailable", "backend_unavailable", "Service temporarily unavailable"],
		["incompatible", "backend_incompatible", "Extension update required"],
	] as const)(
		"contains one %s widget without affecting its sibling",
		(state, code, title) => {
			silenceReactError();
			function BrokenWidget(): never {
				throw new PluginAvailabilityError(state, {
					code,
					requestId: "request-123",
				});
			}

			render(
				<>
					<PluginContributionBoundary
						plugin="example"
						contributionId="traffic"
						kind="widget"
					>
						<BrokenWidget />
					</PluginContributionBoundary>
					<PluginContributionBoundary
						plugin="other"
						contributionId="healthy"
						kind="widget"
					>
						<div>Healthy sibling</div>
					</PluginContributionBoundary>
				</>,
			);

			expect(screen.getByText(title)).toBeTruthy();
			expect(screen.getByText("Healthy sibling")).toBeTruthy();
			expect(screen.getByText(/request-123/)).toBeTruthy();
		},
	);

	it("sanitizes an unexpected route failure", () => {
		silenceReactError();
		function FailedRoute(): never {
			throw new Error("secret product failure");
		}

		render(
			<PluginContributionBoundary
				plugin="example"
				contributionId="overview"
				kind="route"
			>
				<FailedRoute />
			</PluginContributionBoundary>,
		);
		expect(screen.getByText("Extension failed to load")).toBeTruthy();
		expect(screen.queryByText(/secret product failure/)).toBeNull();
	});

	it("renders the canonical loading state while a lazy contribution resolves", () => {
		const Pending = lazy(() => new Promise<never>(() => undefined));
		render(
			<PluginContributionBoundary
				plugin="example"
				contributionId="overview"
				kind="route"
			>
				<Pending />
			</PluginContributionBoundary>,
		);
		expect(
			screen.getByRole("status", { name: "Loading extension route" }),
		).toBeTruthy();
	});

	it("waits for a compatible declared backend before rendering", async () => {
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return Response.json({
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 1,
			});
		});
		const runtime = createPluginRuntime({
			getAccessToken: () => "host-token",
			fetch: fetchMock as typeof fetch,
		});
		await act(async () => {
			render(
				<PluginRuntimeProvider runtime={runtime}>
					<PluginContributionBoundary
						plugin="example"
						contributionId="overview"
						kind="route"
						services={[{ alias: "api" }]}
					>
						<div>Compatible contribution</div>
					</PluginContributionBoundary>
				</PluginRuntimeProvider>,
			);
		});

		expect(screen.getByText("Compatible contribution")).toBeTruthy();
		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"/api/plugins/example/api/.well-known/capabilities",
		);
	});

	it("contains an automatically detected incompatible backend", async () => {
		silenceReactError();
		const runtime = createPluginRuntime({
			getAccessToken: () => "host-token",
			fetch: vi.fn(async () =>
				Response.json(
					{ code: "backend_incompatible", requestId: "untrusted-upstream-id" },
					{
						status: 426,
						headers: {
							"content-type": "application/problem+json",
							"x-request-id": "request-426",
						},
					},
				),
			) as typeof fetch,
		});
		render(
			<PluginRuntimeProvider runtime={runtime}>
				<PluginContributionBoundary
					plugin="example"
					contributionId="overview"
					kind="route"
					services={[{ alias: "api" }]}
				>
					<div>Must not render</div>
				</PluginContributionBoundary>
				<div>Healthy host sibling</div>
			</PluginRuntimeProvider>,
		);

		expect(await screen.findByText("Extension update required")).toBeTruthy();
		expect(screen.queryByText("Must not render")).toBeNull();
		expect(screen.getByText("Healthy host sibling")).toBeTruthy();
		expect(screen.getByText(/request-426/)).toBeTruthy();
	});
});
