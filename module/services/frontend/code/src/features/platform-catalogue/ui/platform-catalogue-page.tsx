"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ChevronDown, ChevronRight, RefreshCw } from "lucide-react";
import { Fragment, useState } from "react";
import type { CollectionReadGrant } from "@/gen/saas/accounts/v1/authorization_pb";
import type {
	CatalogueEntry,
	CatalogueInstallation,
} from "@/gen/saas/accounts/v1/platform_admin_pb";
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
	Label,
	Skeleton,
	Switch,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/shared/ui";
import {
	agentReleaseView,
	buildSizeView,
	CATALOGUE_STATES,
	catalogueGaps,
	catalogueStates,
	type FactView,
	installationRevisionView,
	kindLabel,
	publisherView,
	type StateFact,
} from "../model/facts";
import { usePlatformCatalogue } from "../service/queries";

// The expander, the entry, one column per state, installations and build size.
const COLUMNS = 2 + CATALOGUE_STATES.length + 2;

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
	const [expanded, setExpanded] = useState<Set<string>>(new Set());
	const catalogue = usePlatformCatalogue(includeTombstoned);

	const outcome = readOutcome(catalogue.isError, catalogue.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "the catalogue");
	const entries = withheld ? [] : (catalogue.data?.entries ?? []);
	const gaps = catalogueGaps(entries);

	const toggle = (key: string) =>
		setExpanded((current) => {
			const next = new Set(current);
			if (next.has(key)) next.delete(key);
			else next.add(key);
			return next;
		});

	return (
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 data-slot="page-title" className="type-page-title">
						Catalogue
					</h1>
					<p className="text-muted-foreground">
						Every module and solution deployed on this platform: what it
						declares, what runs, and which organizations have it installed.
					</p>
				</div>
				<Button
					variant="outline"
					onClick={() => catalogue.refetch()}
					disabled={catalogue.isFetching}
				>
					<RefreshCw className="h-4 w-4" />
					Refresh
				</Button>
			</div>

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
						<p className="py-10 text-center text-sm text-muted-foreground">
							{readOutcomeMessage(
								outcome,
								"the catalogue",
								"Nothing is deployed: no module is composed and no solution is registered or installed.",
							)}
						</p>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead className="w-8" />
									<TableHead>Entry</TableHead>
									{CATALOGUE_STATES.map((state) => (
										<TableHead key={state}>{state}</TableHead>
									))}
									<TableHead>Installations</TableHead>
									<TableHead>Build size</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{entries.map((entry) => {
									const key = entryKey(entry);
									const open = expanded.has(key);
									return (
										<Fragment key={key}>
											<TableRow>
												<TableCell>
													{entry.installations.length > 0 && (
														<Button
															variant="ghost"
															size="icon"
															aria-expanded={open}
															aria-label={`${open ? "Hide" : "Show"} installations of ${entry.name}`}
															onClick={() => toggle(key)}
														>
															{open ? (
																<ChevronDown className="h-4 w-4" />
															) : (
																<ChevronRight className="h-4 w-4" />
															)}
														</Button>
													)}
												</TableCell>
												<TableCell>
													<div className="flex items-center gap-2">
														<span className="font-medium">{entry.name}</span>
														<Badge variant="outline">
															{kindLabel(entry.kind)}
														</Badge>
													</div>
													<div className="mt-1">
														<Fact view={publisherView(entry)} />
													</div>
												</TableCell>
												{CATALOGUE_STATES.map((state) => (
													<TableCell key={state} className="align-top">
														<StateFacts
															state={state}
															facts={catalogueStates(entry)[state]}
														/>
													</TableCell>
												))}
												<TableCell className="text-sm">
													{entry.installations.length}
												</TableCell>
												<TableCell>
													<Fact view={buildSizeView(entry)} />
												</TableCell>
											</TableRow>
											{open && (
												<TableRow>
													<TableCell />
													<TableCell colSpan={COLUMNS - 1}>
														<InstallationsTable
															name={entry.name}
															installations={entry.installations}
														/>
													</TableCell>
												</TableRow>
											)}
										</Fragment>
									);
								})}
							</TableBody>
						</Table>
					)}
				</CardContent>
			</Card>

			{gaps.length > 0 && (
				<Card>
					<CardHeader>
						<CardTitle className="text-base">Not known to this host</CardTitle>
						<CardDescription>
							What each unavailable cell above means, and what would supply it.
						</CardDescription>
					</CardHeader>
					<CardContent>
						<dl className="space-y-3 text-sm">
							{gaps.map((gap) => (
								<div key={`${gap.fact}:${gap.label}:${gap.detail}`}>
									<dt className="font-medium">
										{gap.fact} — {gap.label}
									</dt>
									<dd className="text-muted-foreground">{gap.detail}</dd>
								</div>
							))}
						</dl>
					</CardContent>
				</Card>
			)}
		</div>
	);
}

function InstallationsTable({
	name,
	installations,
}: {
	name: string;
	installations: CatalogueInstallation[];
}) {
	return (
		<Table aria-label={`Installations of ${name}`}>
			<TableHeader>
				<TableRow>
					<TableHead>Organization</TableHead>
					<TableHead>Agent release</TableHead>
					<TableHead>Revision</TableHead>
					<TableHead>Teams exposed</TableHead>
					<TableHead>Installed</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{installations.map((item) => (
					<TableRow key={item.installation?.id}>
						<TableCell className="font-medium">{item.orgName}</TableCell>
						<TableCell>
							<Fact view={agentReleaseView(item)} />
						</TableCell>
						<TableCell>
							<Fact view={installationRevisionView(item)} />
						</TableCell>
						<TableCell>
							<TeamGrants
								granted={item.grantedTeams}
								inherited={item.inheritedTeams}
							/>
						</TableCell>
						<TableCell className="text-sm text-muted-foreground">
							{formatDate(
								item.installation?.createdAt
									? timestampDate(item.installation.createdAt).toISOString()
									: undefined,
							)}
						</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}
