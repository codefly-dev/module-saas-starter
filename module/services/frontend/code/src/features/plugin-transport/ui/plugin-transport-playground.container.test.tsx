import type { FrontendServiceAllowlistEntry } from "@codefly-dev/saas-plugin-contract";
import {
	createPluginRuntime,
	PluginRuntimeProvider,
} from "@codefly-dev/ui/plugin-host/runtime";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import Page from "@/app/admin/platform/plugin-transport/page";
import { renderInApp } from "@/test/container";
import {
	handlePluginBffRequest,
	type PluginBffDependencies,
} from "../../../../server/plugin-bff";
import { transportDemoPlugin } from "../model/demo-plugin";

vi.mock("@/components/auth/role-gate", () => ({
	RoleGate: ({ children }: { children: ReactNode }) => children,
}));

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

const TOKEN = "a".repeat(32);

/** The demo plugin's own requirements, as an installing composition binds them. */
function entry(alias: string): FrontendServiceAllowlistEntry | null {
	const requirement = (transportDemoPlugin.services ?? []).find(
		(service) => service.alias === alias,
	);
	if (!requirement) return null;
	return {
		...requirement,
		plugin: transportDemoPlugin.name,
		target: {
			module: "demo-module",
			service: "demo-api",
			endpoint: requirement.protocol,
		},
	};
}

/**
 * The playground, driven end to end: the published client transport, the host's
 * real BFF policy, and one stub upstream. Nothing between the press and the
 * forwarded answer is faked — which is the point, because the defect this page
 * exists for lived in the two path rules the client and the BFF each own.
 */
function render(plugin: string = transportDemoPlugin.name) {
	const upstream = vi.fn(
		async (target: RequestInfo | URL, init?: RequestInit) => {
			void init;
			return new Response(
				JSON.stringify({ upstream: new URL(String(target)).pathname }),
				{ headers: { "content-type": "application/json" } },
			);
		},
	);
	const overrides: Partial<PluginBffDependencies> = {
		fetch: upstream as typeof fetch,
		requestID: () => "request-1",
		resolve: (candidate, alias) => {
			const installed = candidate === plugin ? entry(alias) : null;
			return installed
				? {
						ok: true,
						value: { entry: installed, baseURL: "http://demo.internal" },
					}
				: { ok: false, reason: "not_installed" };
		},
	};
	// The browser half: the transport builds the BFF URL, and this fetch hands
	// it to the route handler the real request would reach.
	const runtime = createPluginRuntime({
		getAccessToken: () => TOKEN,
		fetch: async (input, init) => {
			const url = new URL(String(input), "http://localhost:3000");
			// What the route's catch-all hands the handler: decoded segments
			// after `/api/plugins/{plugin}/{alias}`.
			const [, , , bffPlugin, alias, ...path] = url.pathname
				.split("/")
				.map((segment) => decodeURIComponent(segment));
			return handlePluginBffRequest(
				new Request(url, init),
				{ plugin: bffPlugin, alias, path },
				overrides,
			);
		},
	});
	return {
		upstream,
		...renderInApp(
			<PluginRuntimeProvider runtime={runtime}>
				<Page />
			</PluginRuntimeProvider>,
		),
	};
}

function sendRow(route: string) {
	const row = screen.getByText(route).closest("tr");
	if (!row) throw new Error(`no row for ${route}`);
	fireEvent.click(row.querySelector("button") as HTMLButtonElement);
	return row;
}

describe("the plugin transport playground", () => {
	it("forwards a custom-verb route and shows the upstream's answer", async () => {
		const { upstream } = render();

		const row = sendRow("POST records/records:recover");

		await waitFor(() => {
			expect(row.textContent).toContain("/v1/demo/records:recover");
		});
		// The colon reaches the upstream literal: percent-encoded it would name a
		// different resource and the binding would never be hit.
		expect(String(upstream.mock.calls[0][0])).toBe(
			"http://demo.internal/v1/demo/records:recover",
		);
		expect(
			new Headers(upstream.mock.calls[0][1]?.headers).get("authorization"),
		).toBe(`Bearer ${TOKEN}`);
		expect(row.textContent).toContain("200");
	});

	it("forwards a custom verb on an item, and a connect procedure at the origin root", async () => {
		const { upstream } = render();

		const item = sendRow(
			"POST records/records/record-1/interactions/interaction-1:respond",
		);
		await waitFor(() => {
			expect(item.textContent).toContain(
				"/v1/demo/records/record-1/interactions/interaction-1:respond",
			);
		});

		const procedure = sendRow(
			"POST procedures/demo.procedures.v1.RecordService/RecoverRecord",
		);
		await waitFor(() => {
			expect(procedure.textContent).toContain(
				"/demo.procedures.v1.RecordService/RecoverRecord",
			);
		});
		expect(String(upstream.mock.calls[1][0])).toBe(
			"http://demo.internal/demo.procedures.v1.RecordService/RecoverRecord",
		);
	});

	it("refuses a shape the transport cannot carry before anything leaves the browser", async () => {
		const { upstream } = render();

		const interior = sendRow("POST records/records:recover/commands");
		await waitFor(() => {
			expect(interior.textContent).toContain("refused in the browser");
		});

		const traversal = sendRow("GET records/records/../secrets");
		await waitFor(() => {
			expect(traversal.textContent).toContain("refused in the browser");
		});

		expect(upstream).not.toHaveBeenCalled();
	});

	it("refuses a REST path on a connect alias at the host, naming no endpoint", async () => {
		const { upstream } = render();

		const row = sendRow("POST procedures/records");

		await waitFor(() => {
			expect(row.textContent).toContain("invalid_path");
		});
		expect(row.textContent).toContain("400");
		expect(row.textContent).not.toContain("demo.internal");
		expect(upstream).not.toHaveBeenCalled();
	});

	it("answers 'not installed' for a plugin no composition composed", async () => {
		// The canonical host installs no plugin service, so this is what every
		// row answers here — and it is already proof the shape was carried: the
		// refusal comes from resolution, not from the path gate.
		const { upstream } = render("some-other-plugin");

		const row = sendRow("POST records/records:recover");

		await waitFor(() => {
			expect(row.textContent).toContain("plugin_service_not_found");
		});
		expect(row.textContent).toContain("404");
		expect(upstream).not.toHaveBeenCalled();
	});
});
