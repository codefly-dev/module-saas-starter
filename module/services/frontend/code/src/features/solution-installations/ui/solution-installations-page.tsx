"use client";

import { Layers, RefreshCw } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
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
	TEAM_EXPOSURE_UNAVAILABLE,
} from "../model/installations";
import { useUninstallSolution } from "../service/mutations";
import { useAvailableSolutions, useInstallations } from "../service/queries";
import { InstallSolutionDialog } from "./install-solution-dialog";

/**
 * One fact. A gap reads as its label in muted italics with what it means on
 * hover, so "this host does not know" is never mistaken for a value.
 */
function Fact({ view }: { view: FactView }) {
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
 * The organization's solutions: what it may install, what it has installed and
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

	return (
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 data-slot="page-title" className="type-page-title">
						Solutions
					</h1>
					<p className="text-muted-foreground">
						Install the solutions this organization may use, see what is
						installed and at which release, and decide which teams reach each
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
					title="Select an organization to manage its solutions"
					description="Installations belong to one organization at a time. Pick one from the selector above."
				/>
			) : (
				<>
					<AvailableSection
						query={available}
						onInstall={(solution) => setInstalling(solution)}
					/>
					<InstalledSection
						orgId={orgId}
						query={installed}
						available={available.data ?? []}
						availableKnown={available.isSuccess}
					/>
				</>
			)}

			{installing && (
				<InstallSolutionDialog
					orgId={orgId}
					solution={installing}
					onClose={() => setInstalling(null)}
				/>
			)}
		</div>
	);
}

function AvailableSection({
	query,
	onInstall,
}: {
	query: ReturnType<typeof useAvailableSolutions>;
	onInstall: (solution: AvailableSolution) => void;
}) {
	const { indicator, quiet } = useLoadingPhase(query.isLoading);
	const outcome = readOutcome(query.isError, query.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the available solutions");
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
					<Spinner label="Loading available solutions" />
				) : rows.length === 0 ? (
					<p
						className="py-6 text-center text-sm text-muted-foreground"
						{...(query.isError ? { role: "alert" as const } : {})}
					>
						{readOutcomeMessage(
							outcome,
							"the available solutions",
							"No solution is available to this organization.",
						)}
					</p>
				) : (
					<>
						{stale && (
							<p role="status" className="mb-2 text-sm text-muted-foreground">
								{stale}
							</p>
						)}
						<Table aria-label="Available solutions">
							<TableHeader>
								<TableRow>
									<TableHead>Solution</TableHead>
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
												disabled={row.installed}
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
	available,
	availableKnown,
}: {
	orgId: string;
	query: ReturnType<typeof useInstallations>;
	available: AvailableSolution[];
	availableKnown: boolean;
}) {
	const uninstall = useUninstallSolution(orgId);
	const [confirming, setConfirming] = useState<InstalledRow | null>(null);
	const [refusal, setRefusal] = useState<{
		solution: string;
		reason: string;
	} | null>(null);
	const { indicator, quiet } = useLoadingPhase(query.isLoading);
	const outcome = readOutcome(query.isError, query.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the installed solutions");
	const rows = withheld
		? []
		: installedRows(query.data ?? [], available, availableKnown);

	const name = (row: InstalledRow) =>
		row.solution.kind === "value" ? row.solution.text : "this installation";

	const runUninstall = (row: InstalledRow) => {
		setRefusal(null);
		uninstall.mutate(row.installationId, {
			onSuccess: () => toast.success(`${name(row)} uninstalled`),
			onError: (error) =>
				setRefusal({ solution: name(row), reason: hostRefusal(error) }),
		});
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
						title={`The host refused to uninstall ${refusal.solution}`}
						onDismiss={() => setRefusal(null)}
					>
						{refusal.reason}
					</Banner>
				)}
				{quiet ? null : indicator ? (
					<Spinner label="Loading installed solutions" />
				) : rows.length === 0 ? (
					<p
						className="py-6 text-center text-sm text-muted-foreground"
						{...(query.isError ? { role: "alert" as const } : {})}
					>
						{readOutcomeMessage(
							outcome,
							"the installed solutions",
							"Nothing is installed in this organization.",
						)}
					</p>
				) : (
					<>
						{stale && (
							<p role="status" className="text-sm text-muted-foreground">
								{stale}
							</p>
						)}
						<Table aria-label="Installed solutions">
							<TableHeader>
								<TableRow>
									<TableHead>Solution</TableHead>
									<TableHead>Revision</TableHead>
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
											<Fact view={row.health} />
										</TableCell>
										<TableCell>
											<TeamExposure row={row} name={name(row)} />
										</TableCell>
										<TableCell className="text-sm text-muted-foreground">
											{formatDate(row.installedAt)}
										</TableCell>
										<TableCell className="text-right">
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
		</Card>
	);
}

/**
 * Team exposure for one installation. The action is shown, disabled, with the
 * reason beside it: a missing capability stated where the reader looks for it,
 * rather than a control that silently does nothing.
 */
function TeamExposure({ row, name }: { row: InstalledRow; name: string }) {
	const reasonId = `team-exposure-${row.installationId}`;
	return (
		<div className="max-w-xs space-y-1">
			<Button
				variant="outline"
				size="sm"
				disabled
				aria-label={`Grant a team access to ${name}`}
				aria-describedby={reasonId}
			>
				Grant a team
			</Button>
			<p id={reasonId} className="text-xs text-muted-foreground">
				{TEAM_EXPOSURE_UNAVAILABLE}
			</p>
		</div>
	);
}
