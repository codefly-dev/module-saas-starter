import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import type { RoleGrant } from "@/gen/host/installation/v1/installation_pb";
import {
	AvailableSolutionSchema,
	InstallationStatus,
	OrganizationInstallationSchema,
} from "@/gen/saas/accounts/v1/installations_pb";
import { availableRows } from "./installations";
import {
	catalogueRow,
	isChosen,
	isListed,
	isListedInstalled,
	type ListingScope,
	modulesWithheld,
	pageAccess,
	roleGrantedInstallations,
	setUpRoles,
} from "./listing";

const target = (
	alias: string,
	kind: "module" | "solution",
	extra: { roles?: string[]; installed?: boolean } = {},
) =>
	create(AvailableSolutionSchema, {
		kind,
		targetId: `target-${alias}`,
		routeAlias: alias,
		roles: extra.roles ?? [],
		installed: extra.installed ?? false,
	});

const payroll = target("payroll", "solution");
const ledger = target("ledger", "module", { roles: ["admin", "user"] });
const store = target("store", "module");

const scope = (over: Partial<ListingScope> = {}): ListingScope => ({
	showModules: false,
	offersRead: true,
	offered: new Set<string>(),
	...over,
});

const installation = (
	of: ReturnType<typeof target>,
	status = InstallationStatus.ACTIVE,
	rootScopePath = "",
) =>
	create(OrganizationInstallationSchema, {
		installation: { id: `inst-${of.routeAlias}`, status },
		target: of,
		rootScopePath,
	});

const grant = (role: string) => ({ role, teamId: "team-1" }) as RoleGrant;

describe("which targets each view lists", () => {
	it("lists solutions and module pages on Solutions, and no supporting module", () => {
		expect(isListed(payroll, scope())).toBe(true);
		expect(isListed(ledger, scope())).toBe(true);
		expect(isListed(store, scope())).toBe(false);
		expect(isListed(undefined, scope())).toBe(false);
	});

	it("treats a module the authority offers as one an administrator installs", () => {
		const offered = scope({ offered: new Set(["store"]) });
		expect(isChosen(store, offered)).toBe(true);
		expect(isChosen(store, scope())).toBe(false);
		expect(isChosen(payroll, scope({ offered: undefined }))).toBe(true);
	});

	it("lists the modules that are not chosen on supporting module administration", () => {
		const modules = scope({ showModules: true });
		expect(isListed(store, modules)).toBe(true);
		expect(isListed(ledger, modules)).toBe(true);
		expect(isListed(payroll, modules)).toBe(false);
	});

	it("offers no module until the offers are read, yet still lists every installed module", () => {
		const unread = scope({
			showModules: true,
			offersRead: false,
			offered: undefined,
		});
		expect(modulesWithheld(unread)).toBe(true);
		expect(isListed(store, unread)).toBe(false);
		expect(isListedInstalled(store, unread)).toBe(true);
		expect(isListedInstalled(payroll, unread)).toBe(false);
		// Solutions never waits on the offers to list a solution.
		expect(modulesWithheld(scope({ offersRead: false }))).toBe(false);
	});
});

describe("whose roles the authority grants", () => {
	it("reads grants for an active solution and an active module page only", () => {
		expect(
			roleGrantedInstallations([
				installation(payroll),
				installation(ledger, InstallationStatus.ACTIVE, "root_1"),
				installation(store, InstallationStatus.ACTIVE, "root_2"),
				installation(target("pending", "solution"), InstallationStatus.PENDING),
			]),
		).toEqual(["inst-payroll", "inst-ledger"]);
	});
});

describe("a target's access", () => {
	const installed = [installation(payroll)];

	it("is unknown while the grants are not read", () => {
		expect(
			pageAccess(
				payroll,
				installed,
				() => undefined,
				() => ["admin"],
			),
		).toEqual({
			installationId: "inst-payroll",
			setUp: undefined,
			unheld: undefined,
		});
	});

	it("names the approved roles nobody holds", () => {
		expect(
			pageAccess(
				payroll,
				installed,
				() => [grant("admin")],
				() => ["admin", "user"],
			),
		).toEqual({
			installationId: "inst-payroll",
			setUp: true,
			unheld: ["user"],
		});
	});

	it("reads not set up when nobody holds a role, and nothing for a target not installed", () => {
		expect(
			pageAccess(
				payroll,
				installed,
				() => [],
				() => ["admin"],
			).setUp,
		).toBe(false);
		expect(
			pageAccess(
				ledger,
				installed,
				() => [],
				() => [],
			),
		).toEqual({
			installationId: undefined,
			setUp: undefined,
			unheld: undefined,
		});
	});

	it("gives Set up every role of a module page, and only the unheld roles of a solution", () => {
		expect(setUpRoles(ledger, { unheld: ["user"] })).toEqual(["admin", "user"]);
		const withRoles = target("payroll", "solution", {
			roles: ["admin", "user"],
		});
		expect(setUpRoles(withRoles, { unheld: ["user"] })).toEqual(["user"]);
		expect(setUpRoles(withRoles, {})).toEqual(["admin", "user"]);
	});
});

describe("what a catalogue row says and offers", () => {
	const row = (of: ReturnType<typeof target>) => availableRows([of])[0];

	it("offers Install for a solution that is not installed", () => {
		const view = catalogueRow(row(payroll), {});
		expect(view.state).toEqual({ text: "Not installed" });
		expect(view.action).toEqual({ kind: "install" });
	});

	it("offers nothing more for an installed solution whose roles are all held", () => {
		const view = catalogueRow(
			row(target("payroll", "solution", { installed: true })),
			{ installationId: "inst-1", setUp: true, unheld: [] },
		);
		expect(view.state).toEqual({ text: "Installed", tone: "success" });
		expect(view.action).toEqual({ kind: "none" });
	});

	it("offers Set up for an installed solution a role of which nobody holds", () => {
		const view = catalogueRow(
			row(target("payroll", "solution", { installed: true })),
			{ installationId: "inst-1", setUp: true, unheld: ["user", "auditor"] },
		);
		expect(view.state).toEqual({
			text: "Installed · user, auditor held by nobody",
			tone: "warning",
		});
		expect(view.action).toEqual({ kind: "setUp" });
	});

	it("never offers Install for a module page: Set up until someone holds a role, then Manage access", () => {
		expect(catalogueRow(row(ledger), {})).toMatchObject({
			state: { text: "Module page" },
			action: { kind: "setUp" },
		});
		expect(
			catalogueRow(row(ledger), { installationId: "inst-1", setUp: false }),
		).toMatchObject({
			state: { text: "Nobody has access yet", tone: "warning" },
			action: { kind: "setUp" },
		});
		expect(
			catalogueRow(row(ledger), { installationId: "inst-1", setUp: true }),
		).toMatchObject({
			state: { text: "Set up", tone: "success" },
			action: { kind: "manage", installationId: "inst-1" },
		});
	});

	it("offers no act on supporting module administration", () => {
		expect(catalogueRow(row(ledger), undefined)).toMatchObject({
			state: { text: "Not installed" },
			action: { kind: "none" },
		});
		expect(
			catalogueRow(
				row(target("store", "module", { installed: true })),
				undefined,
			),
		).toMatchObject({
			state: { text: "Installed", tone: "success" },
			action: { kind: "none" },
		});
	});
});
