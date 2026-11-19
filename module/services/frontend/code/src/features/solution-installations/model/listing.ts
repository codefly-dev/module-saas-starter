import type { RoleGrant } from "@/gen/host/installation/v1/installation_pb";
import {
	type AvailableSolution,
	InstallationStatus,
	type OrganizationInstallation,
} from "@/gen/saas/accounts/v1/installations_pb";
import { type AvailableRow, isModulePage } from "./installations";

/**
 * Which of the two views is on screen and what the authority has answered
 * about the targets an administrator chooses (ListOffers): the solutions.
 * Every module is installed by composition; a module page (a module declaring
 * roles) is listed with the solutions as an entry whose access is set up,
 * never installed. Supporting module administration lists the modules.
 */
export interface ListingScope {
	showModules: boolean;
	/** Whether the offers are read: until then no module is classified. */
	offersRead: boolean;
	offered: ReadonlySet<string> | undefined;
}

type Target = AvailableSolution | undefined;

/**
 * Until the offers are read, supporting module administration offers no
 * module and lists what is installed without saying which is a module page:
 * only that classification waits on the authority.
 */
export function modulesWithheld(scope: ListingScope): boolean {
	return scope.showModules && !scope.offersRead;
}

/** A target an administrator installs: a solution, or one the authority offers. */
export function isChosen(target: Target, scope: ListingScope): boolean {
	return (
		target !== undefined &&
		(target.kind === "solution" ||
			scope.offered?.has(target.routeAlias) === true)
	);
}

/** Whether the catalogue of the view on screen lists this target. */
export function isListed(target: Target, scope: ListingScope): boolean {
	if (!scope.showModules)
		return isChosen(target, scope) || isModulePage(target);
	return (
		!modulesWithheld(scope) &&
		target !== undefined &&
		target.kind === "module" &&
		!isChosen(target, scope)
	);
}

/** Whether the installed list of the view on screen lists this target. */
export function isListedInstalled(
	target: Target,
	scope: ListingScope,
): boolean {
	return modulesWithheld(scope)
		? target !== undefined && target.kind === "module"
		: isListed(target, scope);
}

/**
 * The installations the authority grants roles on: an active solution, or an
 * active module page, whose composition installation carries a host scope
 * root while its access is still its roles.
 */
export function roleGrantedInstallations(
	installed: readonly OrganizationInstallation[],
): string[] {
	return installed.flatMap(({ installation, rootScopePath, target }) =>
		installation?.status === InstallationStatus.ACTIVE &&
		(!rootScopePath || isModulePage(target))
			? [installation.id]
			: [],
	);
}

/**
 * A target's active installation, whether anyone holds a role on it (set up,
 * or not yet), and which roles its consent approved that nobody holds: what
 * Set up gives a team. Each is undefined while its read has not answered.
 */
export interface PageAccess {
	installationId?: string;
	setUp?: boolean;
	unheld?: string[];
}

export function pageAccess(
	page: AvailableSolution,
	installed: readonly OrganizationInstallation[],
	grantsOf: (installationId: string) => readonly RoleGrant[] | undefined,
	approvedOf: (installationId: string) => readonly string[] | undefined,
): PageAccess {
	const id = installed.find(
		(row) =>
			row.target?.targetId === page.targetId &&
			row.installation?.status === InstallationStatus.ACTIVE,
	)?.installation?.id;
	const grants = id ? grantsOf(id) : undefined;
	const approved = id ? approvedOf(id) : undefined;
	return {
		installationId: id,
		setUp: grants ? grants.length > 0 : undefined,
		unheld:
			grants && approved
				? approved.filter((role) => !grants.some((g) => g.role === role))
				: undefined,
	};
}

/** The roles Set up gives teams: all of a module page's, or a solution's that nobody holds. */
export function setUpRoles(
	target: AvailableSolution,
	access: PageAccess,
): string[] {
	return isModulePage(target) ? target.roles : (access.unheld ?? target.roles);
}

/** Where a catalogue row stands, as its State cell says it. */
export interface CatalogueState {
	text: string;
	/** Absent: a plain outline, for a state that asks nothing of anyone. */
	tone?: "success" | "warning";
}

/** The one act a catalogue row offers, if any. */
export type CatalogueAction =
	| { kind: "none" }
	| { kind: "install" }
	| { kind: "setUp" }
	| { kind: "manage"; installationId: string };

/** A catalogue row with what its State and Action cells show. */
export interface CatalogueRow extends AvailableRow {
	state: CatalogueState;
	action: CatalogueAction;
}

/**
 * What one catalogue row says and offers. `access` is absent on supporting
 * module administration, where nothing is installed or set up: every row
 * there reads installed or not, and offers no act.
 */
export function catalogueRow(
	row: AvailableRow,
	access: PageAccess | undefined,
): CatalogueRow {
	const unheld = access?.unheld ?? [];
	if (access && isModulePage(row.solution)) {
		return {
			...row,
			state:
				access.setUp === true
					? { text: "Set up", tone: "success" }
					: access.setUp === false
						? { text: "Nobody has access yet", tone: "warning" }
						: { text: "Module page" },
			action:
				access.setUp && access.installationId
					? { kind: "manage", installationId: access.installationId }
					: { kind: "setUp" },
		};
	}
	const state: CatalogueState = !row.installed
		? { text: "Not installed" }
		: unheld.length
			? {
					text: `Installed · ${unheld.join(", ")} held by nobody`,
					tone: "warning",
				}
			: { text: "Installed", tone: "success" };
	const action: CatalogueAction = !access
		? { kind: "none" }
		: !row.installed
			? { kind: "install" }
			: unheld.length
				? { kind: "setUp" }
				: { kind: "none" };
	return { ...row, state, action };
}
