import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, expect, it, vi } from "vitest";
import { renderInApp } from "@/test/container";
import { server } from "@/test/setup";
import { ApiDocsFrame } from "./api-docs-frame";

vi.mock("swagger-ui-react", () => ({
	default: ({ spec }: { spec: { info: { title: string } } }) => (
		<div>{spec.info.title}</div>
	),
}));
afterEach(cleanup);
const url = "*/api/openapi";
it("shows loading then the published documentation", async () => {
	server.use(
		http.get(url, () =>
			HttpResponse.json({ openapi: "3.0.0", info: { title: "Example API" } }),
		),
	);
	renderInApp(<ApiDocsFrame />);
	expect(screen.getByText("Loading API documentation")).toBeTruthy();
	await screen.findByText("Example API");
});
it("keeps failed reads distinct from unpublished documentation and allows recovery", async () => {
	server.use(
		http.get(url, () =>
			HttpResponse.json(
				{ message: "private upstream failure" },
				{ status: 503 },
			),
		),
	);
	renderInApp(<ApiDocsFrame />);
	await screen.findByText("Couldn't load API documentation");
	expect(screen.queryByText("private upstream failure")).toBeNull();
	expect(screen.queryByText("Documentation is not published yet")).toBeNull();
	server.use(
		http.get(url, () =>
			HttpResponse.json({ openapi: "3.0.0", info: { title: "Example API" } }),
		),
	);
	fireEvent.click(screen.getByRole("button", { name: "Try again" }));
	await screen.findByText("Example API");
});
it("explains missing access without offering retry", async () => {
	server.use(http.get(url, () => new HttpResponse(null, { status: 403 })));
	renderInApp(<ApiDocsFrame />);
	await screen.findByText(/Ask your administrator to grant access/);
	expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});
it("explains unpublished documentation without exposing the server message", async () => {
	server.use(
		http.get(url, () =>
			HttpResponse.json({ error: "OpenAPI spec not found" }, { status: 404 }),
		),
	);
	renderInApp(<ApiDocsFrame />);
	await screen.findByText("Documentation is not published yet");
	expect(screen.queryByText("OpenAPI spec not found")).toBeNull();
	expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});
