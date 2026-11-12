"use client";

import { useQuery } from "@tanstack/react-query";
import { Layers, RefreshCw, X } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { teamQueries } from "@/features/teams/service/queries";
import type { RoleGrant } from "@/gen/host/installation/v1/installation_pb";
import {
	type AvailableSolution,
	InstallationStatus,
} from "@/gen/saas/accounts/v1/installations_pb";
import { useAuth } from "@/lib/auth";
import { useOrgMembers } from "@/lib/hooks/use-organizations";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Badge,
	Banner,
	Button,
	buttonVariants,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	DelayedLoading,
	Spinner,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	useLoadingPhase,
} from "@/shared/ui";
import { holdersByRole, statusLine } from "../model/access";
import {
	availableRows,
	type FactView,
	hostRefusal,
	type InstalledRow,
	installedRows,
	isModulePage,
	type TeamGrantRow,
} from "../model/installations";
import {
	useGrantInstallationToTeam,
	useInstallReviewedSolution,
	useRetryInstallation,
	useRevokeInstallationFromTeam,
	useSetUpModulePage,
	useUninstallSolution,
} from "../service/mutations";
import {
	useAuthorityInstallations,
	useAvailableSolutions,
	useInstallations,
	useOfferedTargets,
	useRoleGrants,
} from "../service/queries";
import { GrantTeamDialog } from "./grant-team-dialog";
import { InstallSolutionDialog } from "./install-solution-dialog";
import { SetUpModulePageDialog } from "./set-up-module-page-dialog";
import { TargetName, useTargetTitles } from "./target-name";

/**
 * One fact. A gap reads as its label in muted italics with what it means on
 * hover, so "this host does not know" is never mistaken for a value.
 *
 * Exported for the setup page, which states the same facts about one
 * installation: a second renderer would be a second answer to "is this a value
 * or a gap", and the two would drift.
 */
export function Fact({ view }: { view: FactView }) {
	if (view.kind === "gap") {
		return (
			<span
				className="text-sm italic text-muted-foreground"
				title={view.detail}
			>
				{view.label}
			</span>
		);
	}
	if (view.tone) {
		return (
			<Badge tone={view.tone} dot title={view.detail}>
				{view.text}
			</Badge>
		);
	}
	return (
		<span className="text-sm" title={view.detail}>
			{view.text}
			{view.detail && (
				<span className="block text-xs text-muted-foreground">
					{view.detail}
				</span>
			)}
		</span>
	);
}

/**
 * The organization's modules and solutions: what it may install, what it has installed and
 * at which release, and the runtime acts an administrator performs on them —
 * install, expose to a team, uninstall. Each act is the host's to accept or
 * refuse, and a refusal is shown as the host words it.
 */
export function SolutionInstallationsPage({
	supportingModules = false,
}: {
	supportingModules?: boolean;
}) {
	const { organizationId: orgId = "" } = useAuth();
	return (
		<SolutionInstallationsForOrganization
			key={orgId}
			orgId={orgId}
			showModules={supportingModules}
		/>
	);
}

function SolutionInstallationsForOrganization({
	orgId,
	showModules,
}: {
	orgId: string;
	showModules: boolean;
}) {
	const router = useRouter();
	const [installing, setInstalling] = useState<AvailableSolution | null>(null);
	// The installed target an administrator is updating: a newer release the
	// host offers on its existing installation, reviewed as an install is.
	const [updating, setUpdating] = useState<AvailableSolution | null>(null);
	const available = useAvailableSolutions(orgId);
	const installed = useInstallations(orgId);
	const combinedInstall = useInstallReviewedSolution(orgId);
	// Which targets an administrator installs is the authority's one answer
	// (ListOffers): the solutions. Every module is installed by composition;
	// a module page (a module declaring roles, AvailableSolution.roles) is
	// listed with the solutions as an entry whose access is set up, never
	// installed. Supporting module administration lists the modules. Until
	// the offers are read, supporting module administration lists nothing,
	// and a failed read is said as the host words it.
	const offered = useOfferedTargets(orgId);
	const setUp = useSetUpModulePage(orgId);
	const [settingUp, setSettingUp] = useState<AvailableSolution | null>(null);
	// Until the offers are read, supporting module administration offers no
	// module to install and lists what is installed without saying which is a
	// module page: only that classification waits on the authority.
	const modulesWithheld = showModules && !offered.isSuccess;
	// Who holds which role on each installation the authority grants roles
	// on (no host scope root), named as the organization names its teams and
	// people, and what each cannot reach now: the Solutions page only.
	const held = useAuthorityInstallations(showModules ? "" : orgId);
	const teams = useQuery({
		...teamQueries.list(orgId),
		enabled: !showModules && Boolean(orgId),
	});
	const members = useOrgMembers(showModules ? null : orgId);
	const roleGranted = showModules
		? []
		: (installed.data ?? []).flatMap(({ installation, rootScopePath }) =>
				installation?.status === InstallationStatus.ACTIVE && !rootScopePath
					? [installation.id]
					: [],
			);
	const roleGrants = useRoleGrants(orgId, roleGranted);
	const access: AccessView = {
		...roleGrants,
		unreached: (id) => held.data?.get(id)?.unreached ?? [],
		roles: (id) => held.data?.get(id)?.roles ?? [],
		name: (grant) =>
			grant.teamId
				? (teams.data?.teams.find((team) => team.id === grant.teamId)?.name ??
					grant.teamId)
				: members.data?.find((member) => member.userId === grant.personId)
						?.userEmail || grant.personId,
	};
	// A module page's composition installation, and whether anyone holds a
	// role on it: set up, or not yet.
	const pageAccess = (page: AvailableSolution) => {
		const id = (installed.data ?? []).find(
			(row) =>
				row.target?.targetId === page.targetId &&
				row.installation?.status === InstallationStatus.ACTIVE,
		)?.installation?.id;
		const grants = id ? roleGrants.grants.get(id) : undefined;
		return {
			installationId: id,
			setUp: grants ? grants.length > 0 : undefined,
		};
	};
	const chosen = (target: AvailableSolution | undefined) =>
		target !== undefined &&
		(target.kind === "solution" ||
			offered.data?.has(target.routeAlias) === true);
	const listed = (target: AvailableSolution | undefined) =>
		showModules
			? !modulesWithheld &&
				target !== undefined &&
				target.kind === "module" &&
				!chosen(target)
			: chosen(target) || isModulePage(target);
	const listedInstalled = (target: AvailableSolution | undefined) =>
		modulesWithheld
			? target !== undefined && target.kind === "module"
			: listed(target);

	return (
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 data-slot="page-title" className="type-page-title">
						{showModules ? "Supporting module administration" : "Solutions"}
					</h1>
					<p className="text-muted-foreground">
						{showModules
							? "Modules are part of the platform: every organization has each one, kept up to date by the platform. This page shows which release each is at and whether it is healthy; nothing here installs or removes one."
							: "The solutions this organization can install, and the module pages whose access it sets up. Install a solution after reviewing what each role can do and its supporting modules; a module page is already part of the platform, and nobody can use it until its roles are given to teams."}
					</p>
					{!showModules && (
						<Link
							href="/admin/modules"
							className="text-sm text-muted-foreground underline-offset-4 hover:underline"
						>
							See the supporting modules and their health →
						</Link>
					)}
				</div>
				<div className="flex items-center gap-3">
					<OrgSelector />
					{orgId && (
						<Button
							variant="outline"
							onClick={() => {
								void available.refetch();
								void installed.refetch();
							}}
							disabled={available.isFetching || installed.isFetching}
						>
							<RefreshCw className="h-4 w-4" />
							Refresh
						</Button>
					)}
				</div>
			</div>

			{!orgId ? (
				<EmptyState
					icon={Layers}
					title={
						showModules
							? "Select an organization to manage its modules and solutions"
							: "Select an organization to manage its solutions"
					}
					description="Installations belong to one organization at a time. Pick one from the selector above."
				/>
			) : (
				<>
					{!modulesWithheld && (
						<AvailableSection
							showModules={showModules}
							listed={
								showModules
									? listed
									: (target) => listed(target) && !isModulePage(target)
							}
							query={available}
							installing={
								(Boolean(installing) && offered.isPending) ||
								combinedInstall.isPending ||
								!installed.isSuccess ||
								installed.isFetching
							}
							onInstall={(solution) => {
								combinedInstall.reset();
								setInstalling(solution);
							}}
						/>
					)}
					{!showModules && (
						<AvailableSection
							showModules={false}
							title="Module pages"
							description="Pages of modules that are part of the platform: installed for this organization already, and used only by the people their roles are given to."
							hideWhenEmpty
							listed={(target) => listed(target) && isModulePage(target)}
							query={available}
							installing={false}
							onInstall={() => undefined}
							pageAccess={pageAccess}
							onSetUp={(page) => {
								setUp.reset();
								setSettingUp(page);
							}}
						/>
					)}
					<InstalledSection
						orgId={orgId}
						query={installed}
						showModules={showModules}
						unclassified={modulesWithheld}
						listed={listedInstalled}
						updating={combinedInstall.isPending}
						access={access}
						onUpdate={(target) => {
							combinedInstall.reset();
							setUpdating(target);
						}}
					/>
				</>
			)}

			{offered.isError && (
				<Banner
					tone="danger"
					title="The targets this organization may choose could not be read"
				>
					{hostRefusal(offered.error)}{" "}
					{showModules
						? "A module is listed here only once the authority's offers are read, so no module is listed yet."
						: "A solution is listed once the authority offers it, so no solution is listed yet."}{" "}
					<Button
						size="sm"
						variant="outline"
						onClick={() => void offered.refetch()}
					>
						Try again
					</Button>
				</Banner>
			)}
			<DelayedLoading
				active={(Boolean(installing) || showModules) && offered.isLoading}
				label="Reading which targets declare roles"
			/>
			{updating && (
				<InstallSolutionDialog
					orgId={orgId}
					solution={updating}
					upgrade
					isPending={combinedInstall.isPending}
					error={
						combinedInstall.isError
							? hostRefusal(combinedInstall.error)
							: undefined
					}
					onSubmit={(review, access) =>
						combinedInstall.mutate(
							{ review, access },
							{
								onSuccess: () => {
									toast.success(
										`${updating.routeAlias} updated. Its team access is kept.`,
									);
									setUpdating(null);
								},
								onError: (error) => toast.error(hostRefusal(error)),
							},
						)
					}
					onClose={() => setUpdating(null)}
				/>
			)}
			{settingUp && (
				<SetUpModulePageDialog
					orgId={orgId}
					page={settingUp}
					isPending={setUp.isPending}
					error={setUp.isError ? hostRefusal(setUp.error) : undefined}
					onSubmit={(access) =>
						setUp.mutate(
							{ targetId: settingUp.targetId, access },
							{
								onSuccess: (installation) => {
									if (installation.accessProblems.length)
										toast.warning(
											`Set up, but not every role's access was applied — grant the rest in Set up. ${installation.accessProblems.join(" ")}`,
										);
									else
										toast.success(
											`Access to ${settingUp.routeAlias} is set up. Each role's team holds it now.`,
										);
									setSettingUp(null);
								},
							},
						)
					}
					onClose={() => setSettingUp(null)}
				/>
			)}
			{installing && chosen(installing) && (
				<InstallSolutionDialog
					orgId={orgId}
					solution={installing}
					isPending={combinedInstall.isPending}
					error={
						combinedInstall.isError
							? hostRefusal(combinedInstall.error)
							: undefined
					}
					onSubmit={(review, access) =>
						combinedInstall.mutate(
							{ review, access },
							{
								onSuccess: (installation) => {
									if (installation.accessProblems.length)
										toast.warning(
											`Installed, but not every role's access was applied — grant the rest in Set up. ${installation.accessProblems.join(" ")}`,
										);
									else
										toast.success(
											installation.status === InstallationStatus.ACTIVE
												? "Installed. Each role's team holds it now."
												: "Setup is pending. Each role's team receives it once setup is active.",
										);
									setInstalling(null);
									router.push(`/admin/solutions/${installation.id}`);
								},
								onError: (error) => toast.error(hostRefusal(error)),
							},
						)
					}
					onClose={() => setInstalling(null)}
				/>
			)}
		</div>
	);
}

function AvailableSection({
	showModules,
	listed,
	query,
	installing,
	onInstall,
	onSetUp,
	pageAccess,
	title = showModules ? "Available" : "Solutions",
	description = showModules
		? "What the platform offers this organization right now, at the release in use."
		: "The solutions your organization can install.",
	hideWhenEmpty = false,
}: {
	showModules: boolean;
	title?: string;
	description?: string;
	/** Renders nothing when no row is listed and the read succeeded. */
	hideWhenEmpty?: boolean;
	listed: (target: AvailableSolution | undefined) => boolean;
	query: ReturnType<typeof useAvailableSolutions>;
	installing: boolean;
	onInstall: (solution: AvailableSolution) => void;
	/** Sets up a module page's access; absent where pages are not set up. */
	onSetUp?: (page: AvailableSolution) => void;
	/** A module page's installation, and whether anyone holds a role on it. */
	pageAccess?: (page: AvailableSolution) => {
		installationId?: string;
		setUp?: boolean;
	};
}) {
	const { indicator, quiet } = useLoadingPhase(query.isLoading);
	const outcome = readOutcome(query.isError, query.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the available modules and solutions");
	const rows = withheld ? [] : availableRows((query.data ?? []).filter(listed));
	const titles = useTargetTitles();
	if (hideWhenEmpty && query.isSuccess && rows.length === 0) return null;

	return (
		<Card>
			<CardHeader>
				<CardTitle className="text-base">{title}</CardTitle>
				<CardDescription>{description}</CardDescription>
			</CardHeader>
			<CardContent>
				{quiet ? null : indicator ? (
					<Spinner label="Loading available modules and solutions" />
				) : rows.length === 0 ? (
					<p
						className="py-6 text-center text-sm text-muted-foreground"
						{...(query.isError ? { role: "alert" as const } : {})}
					>
						{readOutcomeMessage(
							outcome,
							"the available modules and solutions",
							showModules
								? "No module or solution is available to this organization. Ask your platform operator to make one available, then refresh this list."
								: "No solution or module page is available to this organization. Ask your platform operator to make one available, then refresh this list.",
						)}
						{query.isError && ` ${hostRefusal(query.error)}`}
					</p>
				) : (
					<>
						{stale && (
							<p role="status" className="mb-2 text-sm text-muted-foreground">
								{stale} {hostRefusal(query.error)}
							</p>
						)}
						<Table
							aria-label={
								showModules
									? "Available modules and solutions"
									: `Available ${title.toLowerCase()}`
							}
						>
							<TableHeader>
								<TableRow>
									<TableHead>
										{showModules
											? "Module or solution"
											: title === "Module pages"
												? "Module page"
												: "Solution"}
									</TableHead>
									<TableHead>{showModules ? "Release" : "Version"}</TableHead>
									{showModules && <TableHead>Generation</TableHead>}
									<TableHead>State</TableHead>
									<TableHead className="text-right">Action</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{rows.map((row) => (
									<TableRow key={row.targetId}>
										<TableCell className="font-medium">
											<TargetName alias={row.alias} titles={titles} />
										</TableCell>
										{showModules ? (
											<>
												<TableCell className="font-mono text-sm">
													{row.release}
												</TableCell>
												<TableCell className="text-sm text-muted-foreground">
													{row.generation}
												</TableCell>
											</>
										) : (
											<TableCell className="text-sm" title={row.release}>
												{row.solution.releaseVersion}
											</TableCell>
										)}
										<TableCell>
											{onSetUp && isModulePage(row.solution) ? (
												pageAccess?.(row.solution).setUp ? (
													<Badge tone="success" dot>
														Set up
													</Badge>
												) : pageAccess?.(row.solution).setUp === false ? (
													<Badge tone="warning" dot>
														Nobody has access yet
													</Badge>
												) : (
													<Badge variant="outline">Module page</Badge>
												)
											) : row.installed ? (
												<Badge tone="success" dot>
													Installed
												</Badge>
											) : (
												<Badge variant="outline">Not installed</Badge>
											)}
										</TableCell>
										<TableCell className="text-right">
											{showModules ? null : onSetUp &&
												isModulePage(row.solution) ? (
												pageAccess?.(row.solution).setUp &&
												pageAccess(row.solution).installationId ? (
													<Link
														href={`/admin/solutions/${pageAccess(row.solution).installationId}`}
														aria-label={`Manage access to ${row.alias}`}
														className={buttonVariants({
															variant: "outline",
															size: "sm",
														})}
													>
														Manage access
													</Link>
												) : (
													<Button
														size="sm"
														aria-label={`Set up access to ${row.alias}`}
														onClick={() => onSetUp(row.solution)}
													>
														Set up
													</Button>
												)
											) : row.installed ? null : (
												<Button
													size="sm"
													disabled={installing}
													aria-label={`Install ${row.alias}`}
													onClick={() => onInstall(row.solution)}
												>
													Install
												</Button>
											)}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</>
				)}
			</CardContent>
		</Card>
	);
}

/**
 * Who an uninstall takes access from, in one phrase ("expenses-editor and
 * expenses-viewer": the teams or people holding a role), from the role holders the table already shows. Empty when
 * nobody holds a role or the holders are not read yet. The dialog said only
 * "Access through this installation is revoked", which names nobody.
 */
export function uninstallLosers(row: InstalledRow, access: AccessView): string {
	const grants = access.grants.get(row.installationId);
	if (!grants) return "";
	const names = grants.map((grant) => ({
		name: access.name(grant),
		id: grant.teamId || grant.personId,
	}));
	// A holder whose name is not read yet would be shown as its id: say the
	// general sentence rather than name anyone by an id.
	if (names.some(({ name, id }) => !name || name === id)) return "";
	const holders = [...new Set(names.map(({ name }) => name))].sort();
	if (holders.length === 0) return "";
	if (holders.length === 1) return `${holders[0]} loses`;
	return `${holders.slice(0, -1).join(", ")} and ${holders[holders.length - 1]} lose`;
}

function InstalledSection({
	showModules,
	unclassified,
	listed,
	orgId,
	query,
	updating,
	onUpdate,
	access,
}: {
	/** Who holds which role, and what each cannot reach, by installation. */
	access: AccessView;
	orgId: string;
	query: ReturnType<typeof useInstallations>;
	showModules: boolean;
	/** Every installed module is listed: which is a module page is not read yet. */
	unclassified: boolean;
	listed: (target: AvailableSolution | undefined) => boolean;
	updating: boolean;
	onUpdate: (target: AvailableSolution) => void;
}) {
	const uninstall = useUninstallSolution(orgId);
	const retry = useRetryInstallation(orgId);
	const revoke = useRevokeInstallationFromTeam(orgId);
	const grant = useGrantInstallationToTeam(orgId);
	const [confirming, setConfirming] = useState<InstalledRow | null>(null);
	const [granting, setGranting] = useState<InstalledRow | null>(null);
	const [refusal, setRefusal] = useState<{
		title: string;
		reason: string;
	} | null>(null);
	const { indicator, quiet } = useLoadingPhase(query.isLoading);
	const outcome = readOutcome(query.isError, query.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the installed modules and solutions");
	const rows = withheld
		? []
		: installedRows((query.data ?? []).filter((row) => listed(row.target)));
	const titles = useTargetTitles();

	const name = (row: InstalledRow) =>
		row.solution.kind === "value" ? row.solution.text : "this installation";
	const losers = confirming ? uninstallLosers(confirming, access) : "";

	const runRetry = (row: InstalledRow) => {
		setRefusal(null);
		retry.mutate(row.installationId, {
			onSuccess: (installation) =>
				installation.accessProblems.length
					? toast.warning(
							`${name(row)}: not every role's access was applied — grant the rest in Set up. ${installation.accessProblems.join(" ")}`,
						)
					: toast.success(`${name(row)} installation pending`),
			onError: (error) =>
				setRefusal({
					title: `The host refused to retry ${name(row)}`,
					reason: hostRefusal(error),
				}),
		});
	};

	const runUninstall = (row: InstalledRow) => {
		setRefusal(null);
		uninstall.mutate(row.installationId, {
			onSuccess: () => toast.success(`${name(row)} uninstalled`),
			onError: (error) =>
				setRefusal({
					title: `The host refused to uninstall ${name(row)}`,
					reason: hostRefusal(error),
				}),
		});
	};

	const runRevoke = (row: InstalledRow, team: TeamGrantRow) => {
		setRefusal(null);
		revoke.mutate(
			{
				installationId: row.installationId,
				teamId: team.teamId,
				roleId: team.roleId,
			},
			{
				onSuccess: () =>
					toast.success(
						`${team.team}'s ${team.role} grant removed from ${name(row)}`,
					),
				onError: (error) =>
					setRefusal({
						title: `The host refused to remove ${team.team} from ${name(row)}`,
						reason: hostRefusal(error),
					}),
			},
		);
	};

	return (
		<Card>
			<CardHeader>
				<CardTitle className="text-base">
					{unclassified ? "Installed modules" : "Installed"}
				</CardTitle>
				<CardDescription>
					{unclassified
						? "Every module installed in this organization, by composition. Which targets are solutions is shown once the authority's offers are read."
						: "What this organization has installed, at which release, and how healthy each installation is right now."}
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-3">
				{refusal && (
					<Banner
						tone="danger"
						title={refusal.title}
						onDismiss={() => setRefusal(null)}
					>
						{refusal.reason}
					</Banner>
				)}
				{quiet ? null : indicator ? (
					<Spinner label="Loading installed modules and solutions" />
				) : rows.length === 0 ? (
					<p
						className="py-6 text-center text-sm text-muted-foreground"
						{...(query.isError ? { role: "alert" as const } : {})}
					>
						{readOutcomeMessage(
							outcome,
							"the installed modules and solutions",
							showModules
								? "Nothing is installed in this organization. Once a module or solution is available above, choose it to install it."
								: "Nothing is installed in this organization. Once a solution or module page is available above, choose it to install it.",
						)}
						{query.isError && ` ${hostRefusal(query.error)}`}
					</p>
				) : (
					<>
						{stale && (
							<p role="status" className="text-sm text-muted-foreground">
								{stale} {hostRefusal(query.error)}
							</p>
						)}
						<Table
							aria-label={
								showModules
									? "Installed modules and solutions"
									: "Installed solutions"
							}
						>
							<TableHeader>
								<TableRow>
									<TableHead>
										{showModules
											? "Module or solution"
											: "Solution or module page"}
									</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Who has access</TableHead>
									<TableHead className="text-right">Action</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{rows.map((row) => (
									<TableRow key={row.installationId}>
										<TableCell className="font-medium">
											<Link
												href={`/admin/solutions/${row.installationId}`}
												className="hover:underline"
											>
												{row.solution.kind === "value" ? (
													<TargetName
														alias={row.solution.text}
														titles={titles}
													/>
												) : (
													<Fact view={row.solution} />
												)}
											</Link>
											{showModules ? (
												<span className="block font-normal">
													<Fact view={row.revision} />
												</span>
											) : (
												row.target && (
													<span
														className="block text-xs font-normal text-muted-foreground"
														title={
															row.revision.kind === "value"
																? row.revision.text
																: undefined
														}
													>
														Version {row.target.releaseVersion}
													</span>
												)
											)}
											<span
												className="block text-xs font-normal text-muted-foreground"
												title={
													row.installedAt
														? undefined
														: "This installation was made before install dates were kept."
												}
											>
												{row.installedAt
													? `Installed ${formatDate(row.installedAt)}`
													: "Install date not recorded"}
											</span>
										</TableCell>
										<TableCell>
											<StatusLine
												row={row}
												unreached={access.unreached(row.installationId)}
											/>
											{row.failureReason && (
												<p className="mt-1 text-sm text-muted-foreground">
													{row.failureReason}
												</p>
											)}
										</TableCell>
										{
											<TableCell>
												{row.status !== InstallationStatus.ACTIVE ? (
													<span className="text-sm text-muted-foreground">
														Given once it is installed
													</span>
												) : row.hasHostScopeRoot ? (
													<TeamExposure
														row={row}
														name={name(row)}
														revoking={revoke.isPending}
														granting={grant.isPending}
														onGrant={() => {
															grant.reset();
															setGranting(row);
														}}
														onRevoke={(team) => runRevoke(row, team)}
													/>
												) : showModules ? (
													<span className="text-sm text-muted-foreground">
														—
													</span>
												) : (
													<RoleHoldersCell row={row} access={access} />
												)}
											</TableCell>
										}
										<TableCell className="text-right">
											<div className="flex flex-col items-end gap-2">
												{(row.status === InstallationStatus.FAILED ||
													(row.status === InstallationStatus.PENDING &&
														!row.hasHostScopeRoot)) && (
													<Button
														variant="outline"
														size="sm"
														disabled={retry.isPending || uninstall.isPending}
														aria-label={`Retry ${name(row)}`}
														onClick={() => runRetry(row)}
													>
														Retry
													</Button>
												)}
												{row.upgradeAvailable &&
													row.target &&
													row.status === InstallationStatus.ACTIVE && (
														<Button
															size="sm"
															disabled={updating || uninstall.isPending}
															aria-label={`Update ${name(row)}`}
															onClick={() => {
																if (row.target) onUpdate(row.target);
															}}
														>
															Review update
														</Button>
													)}
												<Link
													href={`/admin/solutions/${row.installationId}`}
													aria-label={`Manage access to ${name(row)}`}
													className={buttonVariants({
														variant: "outline",
														size: "sm",
													})}
												>
													Manage access
												</Link>
												{/* A module is installed by composition; only its
												    delivered withdrawal retires it. */}
												{row.target?.kind !== "module" && (
													<Button
														variant="outline"
														size="sm"
														disabled={uninstall.isPending}
														aria-label={`Uninstall ${name(row)}`}
														onClick={() => setConfirming(row)}
													>
														Uninstall
													</Button>
												)}
											</div>
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</>
				)}
			</CardContent>
			<AlertDialog
				open={confirming !== null}
				onOpenChange={(open) => !open && setConfirming(null)}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							Uninstall {confirming ? name(confirming) : ""}
						</AlertDialogTitle>
						<AlertDialogDescription>
							{losers
								? `${losers} access to ${confirming ? name(confirming) : ""} at once. `
								: "Everyone who holds one of its roles loses access at once. "}
							Its record and audit trail remain. Shared supporting module
							installations are kept.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							variant="destructive"
							onClick={() => {
								if (confirming) runUninstall(confirming);
								setConfirming(null);
							}}
						>
							Uninstall
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
			{granting && (
				<GrantTeamDialog
					orgId={orgId}
					solution={name(granting)}
					isPending={grant.isPending}
					error={grant.isError ? hostRefusal(grant.error) : undefined}
					onSubmit={(values) => {
						grant.mutate(
							{ installationId: granting.installationId, ...values },
							{
								onSuccess: () => {
									toast.success(`Team grant recorded for ${name(granting)}`);
									setGranting(null);
								},
								onError: (error) =>
									toast.error(
										`The host refused the grant: ${hostRefusal(error)}`,
									),
							},
						);
					}}
					onClose={() => setGranting(null)}
				/>
			)}
		</Card>
	);
}

/**
 * Where one installation stands, on one line: the host's status, readiness
 * and health, an update the authority offers, and what it cannot reach now.
 * Anything that needs an administrator links to the installation's page,
 * which lists why.
 */
function StatusLine({
	row,
	unreached,
}: {
	row: InstalledRow;
	unreached: readonly string[];
}) {
	return (
		<ul
			className="flex max-w-xs flex-wrap gap-1"
			aria-label={`Status of ${row.solution.kind === "value" ? row.solution.text : "this installation"}`}
		>
			{statusLine(row, unreached).map((fact) => (
				<li key={fact.text}>
					{fact.tone === "success" ? (
						<Badge tone="success" dot>
							{fact.text}
						</Badge>
					) : (
						<Link
							href={`/admin/solutions/${row.installationId}`}
							className="hover:underline"
						>
							<Badge
								tone={fact.tone}
								dot
								className="h-auto whitespace-normal text-left"
							>
								{fact.text}
							</Badge>
						</Link>
					)}
				</li>
			))}
		</ul>
	);
}

/** Who holds which role and the people and teams the host names. */
export interface AccessView {
	grants: Map<string, RoleGrant[]>;
	failed: Set<string>;
	unreached: (installationId: string) => readonly string[];
	roles: (installationId: string) => readonly string[];
	name: (grant: RoleGrant) => string;
}

/** Each role and who holds it, by name, as the authority's grants say. */
function RoleHoldersCell({
	row,
	access,
}: {
	row: InstalledRow;
	access: AccessView;
}) {
	if (access.failed.has(row.installationId))
		return (
			<span className="text-sm text-muted-foreground">
				Could not be read.{" "}
				<Link
					href={`/admin/solutions/${row.installationId}`}
					className="underline"
				>
					Open
				</Link>
			</span>
		);
	const grants = access.grants.get(row.installationId);
	if (!grants) return <span className="text-sm text-muted-foreground">…</span>;
	const roles = holdersByRole(
		access.roles(row.installationId),
		grants,
		access.name,
	);
	if (!roles.length)
		return <span className="text-sm text-muted-foreground">No roles</span>;
	return (
		<ul
			className="space-y-0.5 text-sm"
			aria-label={`Who has access to ${row.solution.kind === "value" ? row.solution.text : "this installation"}`}
		>
			{roles.map(({ role, holders }) => (
				<li key={role}>
					<span className="text-muted-foreground">{role}:</span>{" "}
					{holders.length ? (
						holders.join(", ")
					) : (
						<span className="italic text-muted-foreground">nobody</span>
					)}
				</li>
			))}
		</ul>
	);
}

/**
 * Team exposure for one installation: the teams granted here, each removable;
 * the teams that reach it from a grant above, which are real reach but were not
 * granted here and so are not removed here; and the action that grants a team.
 */
function TeamExposure({
	row,
	name,
	revoking,
	granting,
	onGrant,
	onRevoke,
}: {
	row: InstalledRow;
	name: string;
	revoking: boolean;
	granting: boolean;
	onGrant: () => void;
	onRevoke: (team: TeamGrantRow) => void;
}) {
	return (
		<div className="max-w-xs space-y-2">
			{row.grantedTeams.length === 0 && row.inheritedTeams.length === 0 && (
				<p className="text-sm text-muted-foreground">No team</p>
			)}
			{row.grantedTeams.length > 0 && (
				<ul
					className="flex flex-wrap gap-1"
					aria-label={`Teams granted ${name}`}
				>
					{row.grantedTeams.map((team) => (
						<li key={team.key}>
							<Badge
								variant="secondary"
								className="gap-1"
								title={`${team.role} at ${team.scopePath}`}
							>
								{team.team}
								<span className="text-muted-foreground">· {team.role}</span>
								<button
									type="button"
									onClick={() => onRevoke(team)}
									disabled={revoking}
									className="ml-1 hover:text-destructive disabled:opacity-50"
									aria-label={`Remove ${team.team} from ${name}`}
								>
									<X className="h-3 w-3" />
								</button>
							</Badge>
						</li>
					))}
				</ul>
			)}
			{row.inheritedTeams.length > 0 && (
				<p className="text-xs text-muted-foreground">
					Inherited from above:{" "}
					{row.inheritedTeams.map((team) => team.team).join(", ")}
				</p>
			)}
			{/* A module is used through its roles alone: no second access path. */}
			{row.target?.kind !== "module" && (
				<Button
					variant="outline"
					size="sm"
					aria-label={`Grant a team access to ${name}`}
					disabled={granting}
					onClick={onGrant}
				>
					Grant a team
				</Button>
			)}
		</div>
	);
}
