"use client";

import { getCoreRowModel, useReactTable } from "@tanstack/react-table";
import { useMemo } from "react";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { mayKeepRetainedRows, readOutcome } from "@/shared/lib/read-outcome";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	EmptyState,
	useLoadingPhase,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { availableRows } from "../model/installations";
import {
	type CatalogueRow,
	catalogueRow,
	type PageAccess,
} from "../model/listing";
import type { useAvailableSolutions } from "../service/queries";
import {
	type CatalogueMeta,
	MODULE_COLUMNS,
	MODULE_PAGE_COLUMNS,
	SOLUTION_COLUMNS,
} from "./catalogue-columns";
import { ListReadFailure, StaleRowsNotice } from "./list-read-state";
import { useTargetTitles } from "./target-name";

const NO_ROWS: CatalogueRow[] = [];
const SUBJECT = "the available modules and solutions";

/** The three catalogues the page shows: each its own title, columns and words. */
const VARIANTS = {
	solutions: {
		title: "Solutions",
		description: "The solutions your organization can install.",
		label: "Available solutions",
		columns: SOLUTION_COLUMNS,
		emptyHeading: "No solution to install yet",
		empty:
			"No solution or module page is available to this organization. Ask your platform operator to make one available, then refresh this list.",
	},
	modulePages: {
		title: "Module pages",
		description:
			"Pages of modules that are part of the platform: installed for this organization already, and used only by the people their roles are given to.",
		label: "Available module pages",
		columns: MODULE_PAGE_COLUMNS,
		emptyHeading: "No module page yet",
		empty:
			"No solution or module page is available to this organization. Ask your platform operator to make one available, then refresh this list.",
	},
	modules: {
		title: "Available",
		description:
			"What the platform offers this organization right now, at the release in use.",
		label: "Available modules and solutions",
		columns: MODULE_COLUMNS,
		emptyHeading: "No module available yet",
		empty:
			"No module or solution is available to this organization. Ask your platform operator to make one available, then refresh this list.",
	},
} as const;

/**
 * One catalogue: the solutions an administrator installs, the module pages
 * whose access is set up, or (on supporting module administration) what the
 * platform offers. The read's three endings are told apart: nothing offered,
 * not allowed to see, did not complete.
 */
export function CatalogueCard({
	variant,
	listed,
	query,
	installing = false,
	onInstall,
	onSetUp,
	accessOf,
}: {
	variant: keyof typeof VARIANTS;
	listed: (target: AvailableSolution | undefined) => boolean;
	query: ReturnType<typeof useAvailableSolutions>;
	installing?: boolean;
	onInstall: (solution: AvailableSolution) => void;
	/**
	 * Sets up a module page's access, or gives an installed solution's roles
	 * that nobody holds their teams.
	 */
	onSetUp: (page: AvailableSolution) => void;
	/**
	 * A target's installation, whether anyone holds a role on it, and the roles
	 * nobody holds; absent where nothing is installed or set up.
	 */
	accessOf?: (page: AvailableSolution) => PageAccess;
}) {
	const words = VARIANTS[variant];
	const phase = useLoadingPhase(query.isLoading);
	// The table keeps its skeleton for as long as this says: the same wait,
	// the same delay and floor, so the region is busy until its rows are in.
	const waiting = query.isLoading || phase.quiet || phase.indicator;
	const outcome = readOutcome(query.isError, query.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const titles = useTargetTitles();
	const rows = useMemo(() => {
		if (withheld) return NO_ROWS;
		const listedRows = availableRows((query.data ?? []).filter(listed)).map(
			(row) => catalogueRow(row, accessOf?.(row.solution)),
		);
		return listedRows.length ? listedRows : NO_ROWS;
	}, [withheld, query.data, listed, accessOf]);
	const meta: CatalogueMeta = { titles, installing, onInstall, onSetUp };
	const table = useReactTable({
		data: rows,
		columns: words.columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.targetId,
		meta,
	});
	// The module pages card is beside the solutions, not a list of its own:
	// with none to set up it says nothing.
	if (variant === "modulePages" && query.isSuccess && rows.length === 0)
		return null;

	return (
		<Card>
			<CardHeader>
				<CardTitle>{words.title}</CardTitle>
				<CardDescription>{words.description}</CardDescription>
			</CardHeader>
			<CardContent className="space-y-3">
				{!waiting && rows.length === 0 && outcome !== "empty" ? (
					<ListReadFailure
						outcome={outcome}
						subject={SUBJECT}
						error={query.error}
						retrying={query.isFetching}
						onRetry={() => void query.refetch()}
					/>
				) : !waiting && rows.length === 0 && query.isSuccess ? (
					<EmptyState heading={words.emptyHeading} description={words.empty}>
						<Button
							variant="outline"
							size="sm"
							disabled={query.isFetching}
							onClick={() => void query.refetch()}
						>
							Refresh this list
						</Button>
					</EmptyState>
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
							aria-label={words.label}
							aria-busy={waiting || undefined}
						>
							<DataTable table={table} isLoading={query.isLoading} />
						</div>
					</>
				)}
			</CardContent>
		</Card>
	);
}
