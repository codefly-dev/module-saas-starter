"use client";

import { Layers, RefreshCw, X } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { InstallationStatus } from "@/gen/saas/accounts/v1/installation_listing_pb";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { useAuth } from "@/lib/auth";
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
	Spinner,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	useLoadingPhase,
} from "@/shared/ui";
import {
	availableRows,
	type FactView,
	hostRefusal,
	type InstalledRow,
	installedRows,
	type TeamGrantRow,
} from "../model/installations";
import { readinessSummary } from "../model/readiness";
import {
	useGrantInstallationToTeam,
	useInstallSolution,
	useRetryInstallation,
	useRevokeInstallationFromTeam,
	useUninstallSolution,
} from "../service/mutations";
import { useAvailableSolutions, useInstallations } from "../service/queries";
import { GrantTeamDialog } from "./grant-team-dialog";
import { InstallSolutionDialog } from "./install-solution-dialog";

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
export function SolutionInstallationsPage() {
	const { organizationId: orgId = "" } = useAuth();
	return <SolutionInstallationsForOrganization key={orgId} orgId={orgId} />;
}

function SolutionInstallationsForOrganization({ orgId }: { orgId: string }) {
	const [installing, setInstalling] = useState<AvailableSolution | null>(null);
	const available = useAvailableSolutions(orgId);
	const installed = useInstallations(orgId);
	// This owner survives dismissal of the form and reports every host result.
	const install = useInstallSolution(orgId);

	return (
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 data-slot="page-title" className="type-page-title">
						Modules &amp; solutions
					</h1>
					<p className="text-muted-foreground">
						Install the modules and solutions this organization may use, review
						their status and release, and choose which teams can access each
						one.
					</p>
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
					title="Select an organization to manage its modules and solutions"
					description="Installations belong to one organization at a time. Pick one from the selector above."
				/>
			) : (
				<>
					<AvailableSection
						query={available}
						installing={install.isPending}
						onInstall={(solution) => {
							install.reset();
							setInstalling(solution);
						}}
					/>
					<InstalledSection orgId={orgId} query={installed} />
				</>
			)}

			{installing && (
				<InstallSolutionDialog
					orgId={orgId}
					solution={installing}
					isPending={install.isPending}
					error={install.isError ? hostRefusal(install.error) : undefined}
					onSubmit={(values) => {
						install.mutate(
							{ targetId: installing.targetId, values },
							{
								onSuccess: () => {
									toast.success(
										`${installing.routeAlias} installation pending`,
									);
									setInstalling(null);
								},
								onError: (error) =>
									toast.error(
										`The host refused the install: ${hostRefusal(error)}`,
									),
							},
						);
					}}
					onClose={() => setInstalling(null)}
				/>
			)}
		</div>
	);
}

function AvailableSection({
	query,
	installing,
	onInstall,
}: {
	query: ReturnType<typeof useAvailableSolutions>;
	installing: boolean;
	onInstall: (solution: AvailableSolution) => void;
}) {
	const { indicator, quiet } = useLoadingPhase(query.isLoading);
	const outcome = readOutcome(query.isError, query.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the available modules and solutions");
	const rows = withheld ? [] : availableRows(query.data ?? []);

	return (
		<Card>
			<CardHeader>
				<CardTitle className="text-base">Available</CardTitle>
				<CardDescription>
					What this host offers this organization right now, at the release it
					has applied.
				</CardDescription>
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
							"No module or solution is available to this organization. Ask your platform operator to make one available, then refresh this list.",
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
						<Table aria-label="Available modules and solutions">
							<TableHeader>
								<TableRow>
									<TableHead>Module or solution</TableHead>
									<TableHead>Release</TableHead>
									<TableHead>Generation</TableHead>
									<TableHead>State</TableHead>
									<TableHead className="text-right">Action</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{rows.map((row) => (
									<TableRow key={row.targetId}>
										<TableCell className="font-medium">{row.alias}</TableCell>
										<TableCell className="font-mono text-sm">
											{row.release}
										</TableCell>
										<TableCell className="text-sm text-muted-foreground">
											{row.generation}
										</TableCell>
										<TableCell>
											{row.installed ? (
												<Badge tone="success" dot>
													Installed
												</Badge>
											) : (
												<Badge variant="outline">Not installed</Badge>
											)}
										</TableCell>
										<TableCell className="text-right">
											<Button
												size="sm"
												disabled={row.installed || installing}
												aria-label={`Install ${row.alias}`}
												onClick={() => onInstall(row.solution)}
											>
												Install
											</Button>
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

function InstalledSection({
	orgId,
	query,
}: {
	orgId: string;
	query: ReturnType<typeof useInstallations>;
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
	const rows = withheld ? [] : installedRows(query.data ?? []);

	const name = (row: InstalledRow) =>
		row.solution.kind === "value" ? row.solution.text : "this installation";

	const runRetry = (row: InstalledRow) => {
		setRefusal(null);
		retry.mutate(row.installationId, {
			onSuccess: () => toast.success(`${name(row)} installation pending`),
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
				<CardTitle className="text-base">Installed</CardTitle>
				<CardDescription>
					What this organization has installed, at which release, and how
					healthy each installation is right now.
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
							"Nothing is installed in this organization. Once a module or solution is available above, choose it to install it.",
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
						<Table aria-label="Installed modules and solutions">
							<TableHeader>
								<TableRow>
									<TableHead>Module or solution</TableHead>
									<TableHead>Revision</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Ready</TableHead>
									<TableHead>Health</TableHead>
									<TableHead>Teams</TableHead>
									<TableHead>Installed</TableHead>
									<TableHead className="text-right">Action</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{rows.map((row) => (
									<TableRow key={row.installationId}>
										<TableCell className="font-medium">
											<Fact view={row.solution} />
										</TableCell>
										<TableCell>
											<Fact view={row.revision} />
										</TableCell>
										<TableCell>
											<Fact view={row.lifecycle} />
											{row.failureReason && (
												<p className="mt-1 text-sm text-muted-foreground">
													{row.failureReason}
												</p>
											)}
										</TableCell>
										<TableCell>
											<Readiness row={row} />
										</TableCell>
										<TableCell>
											{row.status === InstallationStatus.ACTIVE ? (
												<Fact view={row.health} />
											) : (
												<span className="text-sm text-muted-foreground">
													Unavailable
												</span>
											)}
										</TableCell>
										<TableCell>
											{row.status === InstallationStatus.ACTIVE ? (
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
											) : (
												<span className="text-sm text-muted-foreground">
													Available after activation
												</span>
											)}
										</TableCell>
										<TableCell className="text-sm text-muted-foreground">
											{formatDate(row.installedAt)}
										</TableCell>
										<TableCell className="text-right">
											{row.status === InstallationStatus.FAILED && (
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
											<Link
												href={`/admin/solutions/${row.installationId}`}
												aria-label={`Set up ${name(row)}`}
												className={buttonVariants({
													variant: "outline",
													size: "sm",
												})}
											>
												Set up
											</Link>
											<Button
												variant="outline"
												size="sm"
												disabled={uninstall.isPending}
												aria-label={`Uninstall ${name(row)}`}
												onClick={() => setConfirming(row)}
											>
												Uninstall
											</Button>
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
							Its agent is revoked and its standing grant removed. The record
							and its audit trail remain.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
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
 * Whether the host says this row will work. It sits beside Health deliberately:
 * health answers about the installation RECORD, and on a validation cell ten
 * read healthy while four declared bindings had derived nothing. The link goes
 * to the one place that lists why, because a row cannot carry the conditions.
 */
function Readiness({ row }: { row: InstalledRow }) {
	const summary = readinessSummary(row.readiness);
	if (summary.ready) {
		return (
			<Badge tone="success" dot>
				Ready
			</Badge>
		);
	}
	return (
		<Link
			href={`/admin/solutions/${row.installationId}`}
			className="hover:underline"
		>
			<Badge tone={summary.tone} dot>
				{summary.text}
			</Badge>
		</Link>
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
			<Button
				variant="outline"
				size="sm"
				aria-label={`Grant a team access to ${name}`}
				disabled={granting}
				onClick={onGrant}
			>
				Grant a team
			</Button>
		</div>
	);
}
