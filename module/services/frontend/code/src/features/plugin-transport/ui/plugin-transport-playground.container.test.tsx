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
function render(
	plugin: string = transportDemoPlugin.name,
	// Whether the browser transport was told what this composition installed.
	// The canonical host installs no plugin service, so its own runtime knows
	// nothing about the demo aliases and the host stays the only authority; an
	// installing composition hands them over and the client refuses first.
	installed = false,
) {
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
		...(installed
			? {
					services: (transportDemoPlugin.services ?? []).map((service) => ({
						...service,
						plugin: transportDemoPlugin.name,
					})),
				}
			: {}),
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

function rowFor(route: string): HTMLTableRowElement {
	const row = screen.getByText(route).closest("tr");
	if (!row) throw new Error(`no row for ${route}`);
	return row as HTMLTableRowElement;
}

function press(row: HTMLTableRowElement) {
	fireEvent.click(row.querySelector("button") as HTMLButtonElement);
}

/**
 * Press until the row has actually sent. A mutating row arms on the first press
 * and sends on the second, so a test that pressed once would assert against a
 * request that was never made.
 */
function sendRow(route: string) {
	const row = rowFor(route);
	press(row);
	if (row.textContent?.includes("Press again to send")) press(row);
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

	it("arms a mutating row and sends nothing until it is confirmed", async () => {
		// The plugin name above the table is the operator's to edit. Pointed at a
		// plugin their composition really installed, one press on this row is a
		// real write to that backend under their own credential, so the first
		// press only arms it and names what it will write to.
		const { upstream } = render();
		const row = rowFor("POST records/records:recover");

		press(row);

		expect(row.textContent).toContain("Confirm POST");
		expect(row.textContent).toContain("transport-demo/records");
		expect(upstream).not.toHaveBeenCalled();

		press(row);

		await waitFor(() => {
			expect(row.textContent).toContain("/v1/demo/records:recover");
		});
		expect(upstream).toHaveBeenCalledTimes(1);
		// Sent, and no longer armed: the next press arms again rather than
		// repeating the write.
		expect(row.textContent).not.toContain("Press again to send");
	});

	it("arms a non-GET row the table shows as refused", async () => {
		// `carried` answers for the DEMO declaration, and the request goes to
		// the plugin the operator named — whose requirement for the same alias
		// may be a different protocol with a different service list (issue
		// #1121, N3). So the arming gate reads the method, not the badge: this
		// row is a POST the demo reads as refused, and one press must not send
		// it.
		const { upstream } = render();
		const row = rowFor("POST procedures/records");
		expect(row.textContent).toContain("refused");

		press(row);
		expect(row.textContent).toContain("Press again to send");
		expect(upstream).not.toHaveBeenCalled();
		// Nothing was attempted at all, so the row carries no answer yet.
		expect(row.textContent).not.toContain("invalid_path");

		press(row);
		await waitFor(() => {
			expect(row.textContent).toContain("invalid_path");
		});
	});

	it("sends a read on the first press", async () => {
		// GET is safe and idempotent, so there is nothing to confirm; making the
		// operator press twice for every row would turn the confirmation into
		// noise they learn to click through.
		const { upstream } = render();
		const row = rowFor("GET records/records");

		press(row);

		await waitFor(() => {
			expect(row.textContent).toContain("/v1/demo/records");
		});
		expect(upstream).toHaveBeenCalledTimes(1);
	});

	it("retracts an armed row when the target plugin is renamed", async () => {
		// The confirmation named a plugin. Renaming the target must not carry the
		// armed press over to a different backend.
		const { upstream } = render();
		const row = rowFor("POST records/records:recover");

		press(row);
		expect(row.textContent).toContain("Confirm POST");

		fireEvent.change(screen.getByLabelText("Plugin"), {
			target: { value: "other-plugin" },
		});

		expect(row.textContent).not.toContain("Press again to send");
		expect(upstream).not.toHaveBeenCalled();
	});

	it("refuses a procedure outside the declared services in the browser", async () => {
		// With `routePrefix: "/"` the first path segment is the service name,
		// chosen by the caller. The requirement's declared list is what stops a
		// contributed page reaching every service the upstream origin serves, and
		// a client that was told the requirement applies it before anything
		// leaves the browser.
		const { upstream } = render(transportDemoPlugin.name, true);

		const row = sendRow("POST procedures/demo.other.v1.SecretService/Read");

		await waitFor(() => {
			expect(row.textContent).toContain("refused in the browser");
		});
		expect(upstream).not.toHaveBeenCalled();
	});

	it("carries a connect procedure under a mount prefix", async () => {
		const { upstream } = render();

		const row = sendRow(
			"POST mounted/demo.procedures.v1.RecordService/RecoverRecord",
		);

		await waitFor(() => {
			expect(row.textContent).toContain(
				"/v1/connect/demo.procedures.v1.RecordService/RecoverRecord",
			);
		});
		expect(String(upstream.mock.calls[0][0])).toBe(
			"http://demo.internal/v1/connect/demo.procedures.v1.RecordService/RecoverRecord",
		);
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
