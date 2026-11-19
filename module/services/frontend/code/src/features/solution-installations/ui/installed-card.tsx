"use client";

import { getCoreRowModel, useReactTable } from "@tanstack/react-table";
import { useMemo } from "react";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { mayKeepRetainedRows, readOutcome } from "@/shared/lib/read-outcome";
import {
	Banner,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	EmptyState,
	useLoadingPhase,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { type AccessView, installedName } from "../model/access";
import { type InstalledRow, installedRows } from "../model/installations";
import type { useInstallations } from "../service/queries";
import { GrantTeamDialog } from "./grant-team-dialog";
import {
	INSTALLED_MODULE_COLUMNS,
	INSTALLED_SOLUTION_COLUMNS,
	type InstalledMeta,
} from "./installed-columns";
import {
	RemoveTeamConfirmation,
	UninstallConfirmation,
} from "./installed-confirmations";
import { ListReadFailure, StaleRowsNotice } from "./list-read-state";
import { useTargetTitles } from "./target-name";
import { useInstalledActs } from "./use-installed-acts";

const NO_ROWS: InstalledRow[] = [];
const SUBJECT = "the installed modules and solutions";

/**
 * What the organization has installed: each row's release, where it stands,
 * who has access, and the acts an administrator performs on it.
 */
export function InstalledCard({
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
	const acts = useInstalledActs(orgId);
	const phase = useLoadingPhase(query.isLoading);
	// The table keeps its skeleton for as long as this says: the same wait,
	// the same delay and floor, so the region is busy until its rows are in.
	const waiting = query.isLoading || phase.quiet || phase.indicator;
	const outcome = readOutcome(query.isError, query.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const titles = useTargetTitles();
	const rows = useMemo(() => {
		if (withheld) return NO_ROWS;
		const listedRows = installedRows(
			(query.data ?? []).filter((row) => listed(row.target)),
		);
		return listedRows.length ? listedRows : NO_ROWS;
	}, [withheld, query.data, listed]);
	const meta: InstalledMeta = {
		titles,
		showModules,
		access,
		busy: { ...acts.busy, updating },
		onRetry: acts.retry,
		onUpdate,
		onUninstall: acts.askUninstall,
		onGrant: acts.openGrant,
		onRevoke: acts.askRemove,
	};
	const table = useReactTable({
		data: rows,
		columns: showModules
			? INSTALLED_MODULE_COLUMNS
			: INSTALLED_SOLUTION_COLUMNS,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.installationId,
		meta,
	});

	return (
		<Card>
			<CardHeader>
				<CardTitle role="heading" aria-level={2}>
					{unclassified ? "Installed modules" : "Installed"}
				</CardTitle>
				<CardDescription>
					{unclassified
						? "Every module installed in this organization, by composition. Which of them are solutions is shown once what this organization can install is read."
						: showModules
							? "Check versions and health for installed modules."
							: "Check versions, health and team access for installed solutions."}
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-3">
				{acts.refusal && (
					<Banner
						tone="danger"
						title={acts.refusal.title}
						onDismiss={acts.dismissRefusal}
					>
						{acts.refusal.reason}
					</Banner>
				)}
				{!waiting && rows.length === 0 && outcome !== "empty" ? (
					<ListReadFailure
						outcome={outcome}
						subject={SUBJECT}
						error={query.error}
						retrying={query.isFetching}
						onRetry={() => void query.refetch()}
					/>
				) : !waiting && rows.length === 0 && query.isSuccess ? (
					<EmptyState
						heading="Nothing installed yet"
						description={
							showModules
								? "Nothing is installed in this organization. Once a module or solution is available above, choose it to install it."
								: "Nothing is installed in this organization. Once a solution or module page is available above, choose it to install it."
						}
					/>
				) : (
					<>
						<StaleRowsNotice
							outcome={outcome}
							subject={SUBJECT}
							error={query.error}
						/>
						{/* biome-ignore lint/a11y/useSemanticElements: the kit's DataTable takes no accessible name, so the region around it carries the list's. */}
						<div
							role="region"
							aria-label={
								showModules
									? "Installed modules and solutions"
									: "Installed solutions"
							}
							aria-busy={waiting || undefined}
						>
							<DataTable table={table} isLoading={query.isLoading} />
						</div>
					</>
				)}
			</CardContent>
			<UninstallConfirmation
				row={acts.uninstalling}
				access={access}
				isPending={acts.busy.uninstalling}
				onConfirm={acts.confirmUninstall}
				onCancel={acts.cancelUninstall}
			/>
			<RemoveTeamConfirmation
				removal={acts.removing}
				isPending={acts.busy.revoking}
				onConfirm={acts.confirmRemove}
				onCancel={acts.cancelRemove}
			/>
			{acts.granting && (
				<GrantTeamDialog
					orgId={orgId}
					solution={installedName(acts.granting)}
					isPending={acts.busy.granting}
					error={acts.grantError}
					onSubmit={acts.submitGrant}
					onClose={acts.closeGrant}
				/>
			)}
		</Card>
	);
}
