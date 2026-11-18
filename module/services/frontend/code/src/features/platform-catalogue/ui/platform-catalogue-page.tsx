"use client";

import { Boxes, RefreshCw } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import type { CatalogueEntry } from "@/gen/saas/accounts/v1/platform_admin_pb";
import { CatalogueEntryKind } from "@/gen/saas/accounts/v1/platform_admin_pb";
import {
	mayKeepRetainedRows,
	readOutcome,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Banner,
	Button,
	EmptyState,
	ErrorState,
	Label,
	Page as PageBody,
	PageHeader,
	Panel,
	Section,
	Stack,
	Switch,
} from "@/shared/ui";
import { groupSolutionNavigation } from "@/solutions/solution-groups";
import { catalogueGaps, unbuiltFacts } from "../model/facts";
import { usePlatformCatalogue } from "../service/queries";
import { CatalogueNotes } from "./catalogue-facts";
import { CatalogueTable, InstallationsDialog } from "./catalogue-table";

// The rows when there is no answer: one array for every render, so a table
// built on it is not handed new data each time the page renders.
const NO_ENTRIES: CatalogueEntry[] = [];

function entryKey(entry: CatalogueEntry): string {
	return `${entry.kind}:${entry.name}`;
}

/**
 * The platform Catalogue: every composed module and solution, what was asked
 * for, what was approved, what runs, and which organizations have it
 * installed. Facts this platform holds no record of yet are shown as such,
 * with what would supply them.
 */
export function PlatformCataloguePage() {
	const [includeTombstoned, setIncludeTombstoned] = useState(false);
	const [openKey, setOpenKey] = useState<string | null>(null);
	const catalogue = usePlatformCatalogue(includeTombstoned);

	const outcome = readOutcome(catalogue.isError, catalogue.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the catalogue");
	const entries = withheld
		? NO_ENTRIES
		: (catalogue.data?.entries ?? NO_ENTRIES);
	const gaps = useMemo(() => catalogueGaps(entries), [entries]);
	const unbuilt = useMemo(() => unbuiltFacts(entries), [entries]);
	const sections = useMemo(
		() =>
			groupSolutionNavigation(
				entries.map((entry) => ({
					entry,
					group:
						entry.kind === CatalogueEntryKind.SOLUTION
							? entry.registration?.group
							: undefined,
				})),
			).map((section) => ({
				key: section.key,
				title:
					section.key === "ungrouped"
						? "Modules and other solutions"
						: section.title,
				entries: section.items.map((item) => item.entry),
			})),
		[entries],
	);
	const hasGroups = sections.some((section) => section.key !== "ungrouped");
	// Looked up on every render, so the list of organizations follows the
	// 15-second refresh and closes by itself if its entry is withdrawn.
	const open = entries.find((entry) => entryKey(entry) === openKey);
	const show = useCallback(
		(entry: CatalogueEntry) => setOpenKey(entryKey(entry)),
		[],
	);

	return (
		<PageBody>
			<PageHeader
				title="Catalogue"
				description="Every module and solution deployed on this platform: what was requested, what was approved, what is running, and which organizations have it installed."
				actions={
					<Button
						variant="outline"
						onClick={() => catalogue.refetch()}
						disabled={catalogue.isFetching}
					>
						<RefreshCw className="h-4 w-4" />
						Refresh
					</Button>
				}
			/>

			<Stack direction="row" gap={2} align="center">
				<Switch
					id="include-tombstoned"
					checked={includeTombstoned}
					onCheckedChange={setIncludeTombstoned}
				/>
				<Label htmlFor="include-tombstoned">Show deregistered solutions</Label>
			</Stack>

			{stale && entries.length > 0 && <Banner tone="warning">{stale}</Banner>}

			{catalogue.isLoading ? (
				<CatalogueTable
					entries={NO_ENTRIES}
					isLoading
					onShowInstallations={show}
				/>
			) : entries.length === 0 ? (
				<NothingToShow
					outcome={outcome}
					onRetry={() => void catalogue.refetch()}
				/>
			) : hasGroups ? (
				sections.map((section) => (
					<Section key={section.key} title={section.title}>
						<CatalogueTable
							entries={section.entries}
							onShowInstallations={show}
						/>
					</Section>
				))
			) : (
				<CatalogueTable entries={entries} onShowInstallations={show} />
			)}

			<CatalogueNotes
				title="What this platform doesn't know yet"
				description="What each greyed-out value above means, and what would supply it."
				notes={gaps}
			/>

			{/* Separated from the gaps above on purpose. A gap is this platform
			    failing to learn something it could know; these are facts no part
			    of the platform produces yet, so they are not columns an operator
			    scans for a value that cannot appear. */}
			<CatalogueNotes
				title="Not available on this platform yet"
				description="Facts nothing reports yet, so the table has no column for them. Nothing is misconfigured."
				notes={unbuilt}
			/>

			{open && (
				<InstallationsDialog entry={open} onClose={() => setOpenKey(null)} />
			)}
		</PageBody>
	);
}

/** Why the table is absent: nothing is deployed, or the read did not answer. */
function NothingToShow({
	outcome,
	onRetry,
}: {
	outcome: ReturnType<typeof readOutcome>;
	onRetry: () => void;
}) {
	if (outcome === "empty")
		return (
			<EmptyState
				icon={<Boxes />}
				heading="Nothing is deployed on this platform yet"
				description="A platform operator must deploy a module or solution before an organization can install it. Once one is deployed it appears here."
			/>
		);
	if (outcome === "forbidden")
		return (
			<Panel>
				<ErrorState
					title="You don't have access to the catalogue"
					detail="Something may be deployed; this page can't show it to you. Ask a platform super administrator to give you access."
				/>
			</Panel>
		);
	return (
		<Panel>
			<Stack direction="row" gap={4} align="center" justify="between">
				<ErrorState
					title="The catalogue couldn't be loaded"
					detail="Whether anything is deployed is unknown. If it keeps failing, tell the team that runs this platform."
				/>
				<Button variant="outline" onClick={onRetry}>
					Retry
				</Button>
			</Stack>
		</Panel>
	);
}
