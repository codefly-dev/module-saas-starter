import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";
import { DashboardEditor } from "../dashboard-editor";

// The editor reads the viewer's org through useAuth; pin it so previews and
// aggregate reads resolve rather than sit on the "org unresolved" branch.
vi.mock("@/lib/auth", () => ({ useAuth: () => ({ organizationId: "org-1" }) }));

const STORAGE_KEY = "dashboard:authoring";

// Every aggregate read returns the same single bucket, so a rendered card or a
// preview resolves to a stable, assertable total.
function seedAudit() {
	server.use(
		http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
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
		),
		http.post(rpc("AuditService", "AggregateAuditLog"), () =>
			HttpResponse.json({ buckets: [{ key: "auth.login", count: "42" }] }),
		),
	);
}

beforeEach(() => {
	window.localStorage.clear();
	seedAudit();
});

afterEach(() => {
	cleanup();
	window.localStorage.clear();
});

function addWidget() {
	fireEvent.click(screen.getByRole("button", { name: "Add widget" }));
}

describe("DashboardEditor", () => {
	it("adds a widget and the canvas renders it against live audit data", async () => {
		renderInApp(<DashboardEditor />);

		expect(screen.getByText("My dashboard")).toBeTruthy();
		addWidget();

		// The default widget is a count bar over event types: the canvas resolves
		// it against the aggregate RPC and paints the bucket's value.
		expect(await screen.findByText("42")).toBeTruthy();
	});

	it("edits a widget title and the canvas follows the valid edit live", async () => {
		renderInApp(<DashboardEditor />);
		addWidget();

		const title = await screen.findByLabelText("Widget title");
		fireEvent.change(title, { target: { value: "Logins" } });

		expect(await screen.findByText("Logins")).toBeTruthy();
	});

	it("surfaces a validation error and holds the canvas on the last valid spec", async () => {
		renderInApp(<DashboardEditor />);
		addWidget();

		const title = await screen.findByLabelText("Widget title");
		fireEvent.change(title, { target: { value: "Logins" } });
		expect(await screen.findByText("Logins")).toBeTruthy();

		// An empty title is rejected by validation: the error surfaces inline and
		// the canvas keeps rendering the last committed title.
		fireEvent.change(title, { target: { value: "" } });
		expect(await screen.findByText(/non-empty string/i)).toBeTruthy();
		expect(screen.getByText("Logins")).toBeTruthy();
	});

	it("persists the committed draft to localStorage", async () => {
		renderInApp(<DashboardEditor />);
		addWidget();
		await screen.findByText("42");

		const raw = window.localStorage.getItem(STORAGE_KEY);
		expect(raw).toBeTruthy();
		expect(JSON.parse(raw as string).metrics).toHaveLength(1);
	});

	it("restores a persisted draft on mount", async () => {
		window.localStorage.setItem(
			STORAGE_KEY,
			JSON.stringify({
				version: 1,
				title: "Restored",
				layout: { kind: "grid", columns: 2 },
				metrics: [
					{ title: "Persisted widget", groupBy: "event_type", chart: "bar" },
				],
			}),
		);

		renderInApp(<DashboardEditor />);

		// Both the editor (input value) and the canvas (card title) reflect the
		// restored draft, not the empty initial.
		expect(await screen.findByDisplayValue("Persisted widget")).toBeTruthy();
		expect(screen.getByText("Persisted widget")).toBeTruthy();
	});

	it("removes a widget and clears it from the canvas", async () => {
		renderInApp(<DashboardEditor />);
		addWidget();
		await screen.findByText("42");

		fireEvent.click(screen.getByRole("button", { name: "Remove widget" }));

		await waitFor(() => expect(screen.queryByText("42")).toBeNull());
	});

	it("reorders widgets", async () => {
		renderInApp(<DashboardEditor />);
		addWidget();
		addWidget();

		const titles = await screen.findAllByLabelText("Widget title");
		fireEvent.change(titles[0], { target: { value: "Alpha" } });
		fireEvent.change(titles[1], { target: { value: "Beta" } });

		fireEvent.click(
			screen.getAllByRole("button", { name: "Move widget down" })[0],
		);

		await waitFor(() => {
			const order = screen
				.getAllByLabelText("Widget title")
				.map((input) => (input as HTMLInputElement).value);
			expect(order).toEqual(["Beta", "Alpha"]);
		});
	});

	it("previews a metric against live audit data", async () => {
		renderInApp(<DashboardEditor />);
		addWidget();

		fireEvent.click(await screen.findByRole("button", { name: "Preview" }));

		expect(await screen.findByText(/Total 42 over 1 point/)).toBeTruthy();
	});
});
