import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	OfferRoleSchema,
	ReviewedOfferSchema,
} from "@/gen/host/installation/v1/installation_pb";

const reviewOffer = vi.hoisted(() => vi.fn());
vi.mock("@connectrpc/connect", async (importOriginal) => ({
	...(await importOriginal<typeof import("@connectrpc/connect")>()),
	createClient: () => ({ reviewOffer }),
}));
vi.mock("@/lib/connect/transport", () => ({ apiTransport: {} }));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "example-org" }),
}));

import {
	useRolesDeclaredReview,
	WhenRolesDeclared,
} from "./when-roles-declared";

afterEach(() => {
	cleanup();
	reviewOffer.mockReset();
});

function renderGate(kind: "module" | "solution") {
	const client = new QueryClient();
	render(
		<QueryClientProvider client={client}>
			<WhenRolesDeclared target="example-target" kind={kind}>
				<span>Setup</span>
			</WhenRolesDeclared>
		</QueryClientProvider>,
	);
}

describe("WhenRolesDeclared", () => {
	// Every solution declares roles; nothing is asked.
	it("shows a solution's children without asking the authority", () => {
		renderGate("solution");
		expect(screen.getByText("Setup")).toBeTruthy();
		expect(reviewOffer).not.toHaveBeenCalled();
	});

	// A module page's offer names its roles.
	it("shows a module's children once its offer names roles", async () => {
		reviewOffer.mockResolvedValue(
			create(ReviewedOfferSchema, {
				roles: [
					create(OfferRoleSchema, { name: "installer" }),
					create(OfferRoleSchema, { name: "viewer" }),
				],
			}),
		);
		renderGate("module");
		expect(await screen.findByText("Setup")).toBeTruthy();
		expect(reviewOffer).toHaveBeenCalledWith({
			organizationId: "example-org",
			target: "example-target",
		});
	});

	// The authority refuses the offer of a module that declares no roles.
	it("shows nothing for a module whose offer the authority refuses", async () => {
		reviewOffer.mockRejectedValue(
			new ConnectError(
				"not a composed solution or a module page declaring roles",
				Code.NotFound,
			),
		);
		renderGate("module");
		await vi.waitFor(() => expect(reviewOffer).toHaveBeenCalled());
		expect(screen.queryByText("Setup")).toBeNull();
	});

	// Only NotFound means "declares no roles"; any other failure is no answer.
	it.each([
		["NotFound", Code.NotFound, "declared: false"],
		["Unavailable", Code.Unavailable, "declared: unknown, error: try later"],
	])(
		"reads %s as an answer only when it is NotFound",
		async (_name, code, expected) => {
			reviewOffer.mockRejectedValue(new ConnectError("try later", code));
			function Probe() {
				const { declared, error } = useRolesDeclaredReview(
					"example-target",
					"module",
				);
				if (declared === undefined && !error) return <span>reading</span>;
				return (
					<span>
						{declared === undefined
							? `declared: unknown, error: ${ConnectError.from(error).rawMessage}`
							: `declared: ${declared}`}
					</span>
				);
			}
			render(
				<QueryClientProvider client={new QueryClient()}>
					<Probe />
				</QueryClientProvider>,
			);
			expect(await screen.findByText(expected)).toBeTruthy();
		},
	);
});
