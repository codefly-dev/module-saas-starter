import { cleanup, fireEvent, screen } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderInApp, rpc } from "@/test/container";
import { server } from "@/test/setup";

import { WebhookForm } from "./webhook-form";

afterEach(cleanup);

// The form used to offer a list compiled into this build. That list held four
// names the audit catalog never had (saas.org.updated, saas.org.deleted,
// saas.invite.sent, saas.invite.accepted — the catalog has saas.invitation.*),
// so an endpoint subscribed to one through this form silently never received
// anything; and it could not offer a type a solution declares at runtime, which
// is registered after this build was made. The registry answers both.
function registryReturns(
	types: Array<{
		name: string;
		webhookEligible: boolean;
		deprecated?: boolean;
	}>,
) {
	server.use(
		http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
			HttpResponse.json({
				types: types.map((type) => ({
					name: type.name,
					namespace: type.name.split(".")[0],
					version: 1,
					category: "identity",
					owner: "accounts",
					deprecated: type.deprecated ?? false,
					description: "",
					marksUserJoined: false,
					webhookEligible: type.webhookEligible,
				})),
			}),
		),
	);
}

describe("WebhookForm event choices", () => {
	it("offers exactly the types the registry reports as deliverable", async () => {
		registryReturns([
			{ name: "saas.user.created", webhookEligible: true },
			// Declared by a solution at runtime, granted external delivery: a
			// build-time list could never have carried it.
			{ name: "example.item.created", webhookEligible: true },
			// Declared, but tenant-visible: it is registered and readable in the
			// audit log, and an endpoint must never be offered it.
			{ name: "example.item.closed", webhookEligible: false },
			// Eligible but retired.
			{ name: "saas.legacy.thing", webhookEligible: true, deprecated: true },
		]);

		renderInApp(
			<WebhookForm
				open
				onSubmit={() => {}}
				onCancel={() => {}}
				isPending={false}
			/>,
		);

		expect(await screen.findByText(/user created/i)).toBeTruthy();
		expect(await screen.findByText(/item created/i)).toBeTruthy();
		expect(screen.queryByText(/item closed/i)).toBeNull();
		expect(screen.queryByText(/legacy thing/i)).toBeNull();
	});

	it("says so when nothing in this deployment can be delivered", async () => {
		registryReturns([{ name: "example.item.closed", webhookEligible: false }]);

		renderInApp(
			<WebhookForm
				open
				onSubmit={() => {}}
				onCancel={() => {}}
				isPending={false}
			/>,
		);

		expect(
			await screen.findByText(/No events are available to send/i),
		).toBeTruthy();
	});
});

it("withholds event controls and submission after an access refusal", async () => {
	server.use(
		http.post(rpc("AuditService", "ListAuditEventTypes"), () =>
			HttpResponse.json(
				{ code: "permission_denied", message: "private event scope" },
				{ status: 403 },
			),
		),
	);
	const submit = vi.fn();
	renderInApp(
		<WebhookForm
			open
			onSubmit={submit}
			onCancel={() => {}}
			isPending={false}
		/>,
	);
	expect(
		await screen.findByText(/Ask your organization administrator for access/),
	).toBeTruthy();
	expect(screen.queryByRole("checkbox")).toBeNull();
	expect(
		(
			screen.getByRole("button", {
				name: "Create webhook",
			}) as HTMLButtonElement
		).disabled,
	).toBe(true);
	expect(screen.queryByText(/private event scope/)).toBeNull();
	expect(submit).not.toHaveBeenCalled();
});
it("keeps pending webhook fields and cancellation locked", async () => {
	registryReturns([{ name: "saas.user.created", webhookEligible: true }]);
	const cancel = vi.fn();
	renderInApp(
		<WebhookForm open onSubmit={() => {}} onCancel={cancel} isPending />,
	);
	const event = await screen.findByRole("checkbox", { name: /user created/i });
	expect(event.getAttribute("aria-disabled")).toBe("true");
	expect(
		(screen.getByRole("textbox", { name: "Endpoint URL" }) as HTMLInputElement)
			.disabled,
	).toBe(true);
	fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
	expect(cancel).not.toHaveBeenCalled();
});

it("filters event choices without losing the selected events", async () => {
	registryReturns([
		{ name: "saas.user.created", webhookEligible: true },
		{ name: "example.item.created", webhookEligible: true },
	]);
	renderInApp(
		<WebhookForm
			open
			onSubmit={() => {}}
			onCancel={() => {}}
			isPending={false}
		/>,
	);
	fireEvent.click(
		await screen.findByRole("checkbox", { name: /user created/i }),
	);
	const search = screen.getByRole("textbox", { name: "Search events" });
	fireEvent.change(search, { target: { value: "item" } });
	expect(screen.queryByRole("checkbox", { name: /user created/i })).toBeNull();
	expect(screen.getByRole("checkbox", { name: /item created/i })).toBeTruthy();
	fireEvent.change(search, { target: { value: "" } });
	expect(
		screen
			.getByRole("checkbox", { name: /user created/i })
			.getAttribute("aria-checked"),
	).toBe("true");
});
