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
// The session the page is read as: an organization administrator unless a
// test says otherwise. A cast reads as holding no role (useAuth hides them).
const session = vi.hoisted(() => ({
	orgRole: "admin" as string | undefined,
	platformRole: undefined as string | undefined,
}));
vi.mock("../service/queries", () => ({
	useAvailableSolutions: (orgId: string) => {
		catalogue.asked.push(orgId);
		return { ...catalogue.result, refetch: vi.fn() };
	},
}));
vi.mock("@/lib/auth", () => ({
	useAuth: () => ({ organizationId: "example-org", ...session }),
}));

import {
	useRolesDeclaredReview,
	WhenRolesDeclared,
} from "./when-roles-declared";

afterEach(() => {
	cleanup();
	catalogue.result = {};
	catalogue.asked = [];
	session.orgRole = "admin";
	session.platformRole = undefined;
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

	// Setup on a module page is an administrator's act and the catalogue an
	// administrator's read: a session holding no administrator role — a member,
	// or a cast, which holds none of the person's roles — is offered no Setup
	// and never asks for a read the host would refuse.
	it.each([
		["a member", "member"],
		["a cast session", undefined],
	])("offers %s no Setup on a module page and does not read the catalogue", (_, role) => {
		session.orgRole = role;
		catalogue.result = { data: [row("example-target", ["installer"])] };
		renderGate("module");
		expect(screen.queryByText("Setup")).toBeNull();
		expect(screen.queryByRole("alert")).toBeNull();
		expect(catalogue.asked.every((org) => org === "")).toBe(true);
	});

	it("shows a module's Setup to a platform administrator", () => {
		session.orgRole = undefined;
		session.platformRole = "super_admin";
		catalogue.result = { data: [row("example-target", ["installer"])] };
		renderGate("module");
		expect(screen.getByText("Setup")).toBeTruthy();
	});

	it("shows nothing while the catalogue is being read", () => {
		renderGate("module");
		expect(screen.queryByText("Setup")).toBeNull();
	});

	it("says why when the catalogue could not be read, instead of showing nothing", () => {
		catalogue.result = {
			isError: true,
			error: new ConnectError("registry is down", Code.Unavailable),
		};
		renderGate("module");
		expect(screen.queryByText("Setup")).toBeNull();
		expect(screen.getByRole("alert").textContent).toContain("registry is down");
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
