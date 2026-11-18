"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { EmptyState } from "@codefly-dev/ui/layout";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import type { CollectionReadGrant } from "@/gen/saas/accounts/v1/authorization_pb";
import type {
	CatalogueEntry,
	CatalogueInstallation,
} from "@/gen/saas/accounts/v1/platform_admin_pb";
import { CatalogueEntryKind } from "@/gen/saas/accounts/v1/platform_admin_pb";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	ErrorState,
	Label,
	Page as PageBody,
	PageHeader,
	Skeleton,
	Switch,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { groupSolutionNavigation } from "@/solutions/solution-groups";
import {
	agentReleaseView,
	CATALOGUE_STATES,
	type CatalogueNote,
	catalogueGaps,
	catalogueStates,
	type FactView,
	kindLabel,
	publisherView,
	type StateFact,
	unbuiltFacts,
} from "../model/facts";
import { usePlatformCatalogue } from "../service/queries";

/**
 * One cell. A gap reads as its reason in muted italics with the detail on
 * hover, so a reader never mistakes "this host does not know" for a value.
 */
export function Fact({ view }: { view: FactView }) {
	if (view.kind === "gap") {
		return (
			<span
				className="text-sm italic text-muted-foreground"
				title={view.detail}
				data-fact="gap"
			>
				{view.label}
			</span>
		);
	}
	if (view.tone) {
		return (
			<Badge tone={view.tone} dot title={view.detail} data-fact="value">
				{view.text}
			</Badge>
		);
	}
	return (
		<span className="font-mono text-sm" title={view.detail} data-fact="value">
			{view.text}
		</span>
	);
}

/**
 * One state's facts, stacked and labelled. A state the host holds nothing for
 * reads as its gaps, so a row shows where an entry stands in the progression
 * and where this host cannot tell.
 */
function StateFacts({ state, facts }: { state: string; facts: StateFact[] }) {
	return (
		<dl className="space-y-1" aria-label={state}>
			{facts.map((item) => (
				<div key={item.label}>
					<dt className="text-xs text-muted-foreground">{item.label}</dt>
					<dd>
						<Fact view={item.view} />
					</dd>
				</div>
			))}
		</dl>
	);
}

/**
 * The teams a grant reaches inside an installation, in the two kinds the server
 * keeps apart: granted at this installation, and inherited from a grant above
 * it. Inherited reach is real reach, but it was never granted here, so it is
 * never shown as if it had been.
 */
export function TeamGrants({
	granted,
	inherited,
}: {
	granted: CollectionReadGrant[];
	inherited: CollectionReadGrant[];
}) {
	if (granted.length === 0 && inherited.length === 0) {
		return <span className="text-sm text-muted-foreground">No team</span>;
	}
	const group = (label: string, teams: CollectionReadGrant[]) =>
		teams.length > 0 && (
			<div>
				<p className="text-xs text-muted-foreground">{label}</p>
				<div className="flex flex-wrap gap-1">
					{teams.map((team) => (
						<Badge
							key={team.grant?.id}
							variant="secondary"
							title={`${team.roleName} at ${team.grant?.scopePath || "the organization root"}`}
						>
							{team.subjectLabel}
						</Badge>
					))}
				</div>
			</div>
		);
	return (
		<div className="space-y-1">
			{group("Granted at this installation", granted)}
			{group("Inherited from above", inherited)}
		</div>
	);
}

/** One list of notes under the table: a title, why it is there, and the rows. */
function Notes({
	title,
	description,
	notes,
}: {
	title: string;
	description: string;
	notes: CatalogueNote[];
}) {
	if (notes.length === 0) return null;
	return (
		<Card>
			<CardHeader>
				<CardTitle role="heading" aria-level={2} className="text-base">
					{title}
				</CardTitle>
				<CardDescription>{description}</CardDescription>
			</CardHeader>
			<CardContent>
				<dl className="space-y-3 text-sm">
					{notes.map((note) => (
						<div key={`${note.fact}:${note.label}:${note.detail}`}>
							<dt className="font-medium">
								{note.fact} — {note.label}
							</dt>
							<dd className="text-muted-foreground">{note.detail}</dd>
						</div>
					))}
				</dl>
			</CardContent>
		</Card>
	);
}

function entryKey(entry: CatalogueEntry): string {
	return `${entry.kind}:${entry.name}`;
}

/**
 * The platform Catalogue: every composed module and solution, what it declares,
 * what runs, and which organizations have it installed. Facts this host holds
 * no record of yet are shown as such, with what would supply them.
 */
export function PlatformCataloguePage() {
	const [includeTombstoned, setIncludeTombstoned] = useState(false);
	const [selectedKey, setSelectedKey] = useState<string | null>(null);
	const catalogue = usePlatformCatalogue(includeTombstoned);

	const outcome = readOutcome(catalogue.isError, catalogue.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the catalogue");
	const entries = withheld ? [] : (catalogue.data?.entries ?? []);
	const gaps = catalogueGaps(entries);
	const unbuilt = unbuiltFacts(entries);
	const sections = groupSolutionNavigation(
		entries.map((entry) => ({
			entry,
			group:
				entry.kind === CatalogueEntryKind.SOLUTION
					? entry.registration?.group
					: undefined,
		})),
	);
	const hasGroups = sections.some((section) => section.key !== "ungrouped");

	const selected = entries.find((entry) => entryKey(entry) === selectedKey);

	return (
		<PageBody>
			<PageHeader
				title="Catalogue"
				description="Every module and solution deployed on this platform: what it declares, what runs, and which organizations have it installed."
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

			<div className="flex items-center gap-2">
				<Switch
					id="include-tombstoned"
					checked={includeTombstoned}
					onCheckedChange={setIncludeTombstoned}
				/>
				<Label htmlFor="include-tombstoned">Show deregistered solutions</Label>
			</div>

			{stale && (
				<p role="status" className="text-sm text-muted-foreground">
					{stale}
				</p>
			)}

			<Card>
				<CardContent className="pt-6">
					{catalogue.isLoading ? (
						<Skeleton className="h-48 w-full" />
					) : entries.length === 0 ? (
						catalogue.isError ? (
							<ErrorState
								title="Couldn’t load the catalogue"
								detail={readOutcomeMessage(outcome, "the catalogue", "")}
							/>
						) : (
							<EmptyState
								heading="No applications deployed"
								description="No module or solution has been delivered to this platform. A platform operator must deploy one before an organization can install it."
							/>
						)
					) : (
						<div className="space-y-6">
							{sections.map((section) => (
								<section key={section.key}>
									{hasGroups && (
										<h2 className="mb-3 font-medium">
											{section.key === "ungrouped"
												? "Modules and other solutions"
												: section.title}
										</h2>
									)}
									<CatalogueTable
										entries={section.items.map((item) => item.entry)}
										onSelect={setSelectedKey}
									/>
								</section>
							))}
						</div>
					)}
				</CardContent>
			</Card>

			<Notes
				title="Not known to this host"
				description="What each unavailable cell above means, and what would supply it."
				notes={gaps}
			/>

			{/* Separated from the gaps above on purpose. A gap is this host failing
			    to learn something it could know; these are facts no part of the
			    platform produces yet, so they are not columns an operator scans for
			    a value that cannot appear. */}
			<Notes
				title="Not yet built on this platform"
				description="Facts no component reports yet, so the Catalogue carries no column for them. Nothing is misconfigured."
				notes={unbuilt}
			/>
			<Dialog
				open={!!selected}
				onOpenChange={(open) => {
					if (!open) setSelectedKey(null);
				}}
			>
				<DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-4xl">
					<DialogHeader>
						<DialogTitle>Installations of {selected?.name}</DialogTitle>
						<DialogDescription>
							Organizations using this application and the teams with access.
						</DialogDescription>
					</DialogHeader>
					{selected && (
						<InstallationsTable
							name={selected.name}
							installations={selected.installations}
						/>
					)}
				</DialogContent>
			</Dialog>
		</PageBody>
	);
}

function CatalogueTable({
	entries,
	onSelect,
}: {
	entries: CatalogueEntry[];
	onSelect: (key: string) => void;
}) {
	const columns = useMemo(() => {
		const col = createColumnHelper<CatalogueEntry>();
		return [
			col.accessor("name", {
				header: "Application",
				cell: ({ row }) => (
					<div>
						<div className="flex items-center gap-2">
							<span className="font-medium">{row.original.name}</span>
							<Badge variant="outline">{kindLabel(row.original.kind)}</Badge>
						</div>
						<Fact view={publisherView(row.original)} />
					</div>
				),
			}),
			...CATALOGUE_STATES.map((state) =>
				col.display({
					id: state,
					header: state,
					cell: ({ row }) => (
						<StateFacts
							state={state}
							facts={catalogueStates(row.original)[state]}
						/>
					),
				}),
			),
			col.display({
				id: "installations",
				header: "Installations",
				cell: ({ row }) =>
					row.original.installations.length ? (
						<Button
							variant="outline"
							size="sm"
							aria-label={`Show installations of ${row.original.name}`}
							onClick={() => onSelect(entryKey(row.original))}
						>
							{row.original.installations.length} organizations
						</Button>
					) : (
						"None"
					),
			}),
		];
	}, [onSelect]);
	const table = useReactTable({
		data: entries,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} />;
}

function InstallationsTable({
	name,
	installations,
}: {
	name: string;
	installations: CatalogueInstallation[];
}) {
	const columns = useMemo(() => {
		const col = createColumnHelper<CatalogueInstallation>();
		return [
			col.accessor("orgName", { header: "Organization" }),
			col.display({
				id: "release",
				header: "Release",
				cell: ({ row }) => <Fact view={agentReleaseView(row.original)} />,
			}),
			col.display({
				id: "teams",
				header: "Teams with access",
				cell: ({ row }) => (
					<TeamGrants
						granted={row.original.grantedTeams}
						inherited={row.original.inheritedTeams}
					/>
				),
			}),
			col.display({
				id: "installed",
				header: "Installed",
				cell: ({ row }) =>
					formatDate(
						row.original.installation?.createdAt
							? timestampDate(row.original.installation.createdAt).toISOString()
							: undefined,
					),
			}),
		];
	}, []);
	const table = useReactTable({
		data: installations,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return (
		<section aria-label={`Installations of ${name}`}>
			<DataTable table={table} />
		</section>
	);
}
