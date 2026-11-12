import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	type AvailableSolution,
	AvailableSolutionSchema,
} from "@/gen/saas/accounts/v1/installations_pb";

// A module page's roles are the host's catalogue row (ListAvailableSolutions),
// the read the Solutions page classifies module pages by. The offer review
// answers NotFound for every module ("a module is installed by composition"),
// so it is never asked here.
const catalogue = vi.hoisted(() => ({
	result: {} as {
		data?: unknown[];
		isError?: boolean;
		error?: unknown;
	},
	asked: [] as string[],
}));
vi.mock("../service/queries", () => ({
	useAvailableSolutions: (orgId: string) => {
		catalogue.asked.push(orgId);
		return { ...catalogue.result, refetch: vi.fn() };
	},
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "example-org" }),
}));

import {
	useRolesDeclaredReview,
	WhenRolesDeclared,
} from "./when-roles-declared";

afterEach(() => {
	cleanup();
	catalogue.result = {};
	catalogue.asked = [];
});

const row = (alias: string, roles: string[]): AvailableSolution =>
	create(AvailableSolutionSchema, {
		routeAlias: alias,
		kind: "module",
		roles: roles.map((name) => ({ name })),
	} as never);

function renderGate(kind: "module" | "solution") {
	render(
		<WhenRolesDeclared target="example-target" kind={kind}>
			<span>Setup</span>
		</WhenRolesDeclared>,
	);
}

describe("WhenRolesDeclared", () => {
	// Every solution declares roles; nothing is asked.
	it("shows a solution's children without reading the catalogue", () => {
		renderGate("solution");
		expect(screen.getByText("Setup")).toBeTruthy();
		expect(catalogue.asked).toEqual([""]);
	});

	it("shows a module's children when the catalogue lists it with roles", () => {
		catalogue.result = {
			data: [row("other", []), row("example-target", ["installer", "viewer"])],
		};
		renderGate("module");
		expect(screen.getByText("Setup")).toBeTruthy();
		expect(catalogue.asked).toEqual(["example-org"]);
	});

	it("shows nothing for a module the catalogue lists without roles", () => {
		catalogue.result = { data: [row("example-target", [])] };
		renderGate("module");
		expect(screen.queryByText("Setup")).toBeNull();
	});

	it("shows nothing while the catalogue is being read", () => {
		renderGate("module");
		expect(screen.queryByText("Setup")).toBeNull();
	});

	// A refused read is not "declares none": it stays unknown and says why.
	it("reads a failed catalogue as no answer, with its error", () => {
		const refusal = new ConnectError("try later", Code.Unavailable);
		catalogue.result = { isError: true, error: refusal };
		function Probe() {
			const { declared, error } = useRolesDeclaredReview(
				"example-target",
				"module",
			);
			return (
				<span>
					{declared === undefined ? "unknown" : String(declared)}:
					{ConnectError.from(error).rawMessage}
				</span>
			);
		}
		render(<Probe />);
		expect(screen.getByText("unknown:try later")).toBeTruthy();
	});
});
