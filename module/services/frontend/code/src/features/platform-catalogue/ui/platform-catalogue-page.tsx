"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ChevronDown, ChevronRight, RefreshCw } from "lucide-react";
import { Fragment, useState } from "react";
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
	buildDigestView,
	buildSizeView,
	catalogueGaps,
	declaredReleaseView,
	type FactView,
	generationView,
	installationRevisionView,
	kindLabel,
	publisherView,
	registrationView,
	runningView,
} from "../model/facts";
import { usePlatformCatalogue } from "../service/queries";

const COLUMNS = 10;

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
									<TableHead>Name</TableHead>
									<TableHead>Registration</TableHead>
									<TableHead>Publisher</TableHead>
									<TableHead>Declared release</TableHead>
									<TableHead>Build</TableHead>
									<TableHead>Generation</TableHead>
									<TableHead>Running</TableHead>
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
												</TableCell>
												<TableCell>
													<Fact view={registrationView(entry)} />
												</TableCell>
												<TableCell>
													<Fact view={publisherView(entry)} />
												</TableCell>
												<TableCell>
													<Fact view={declaredReleaseView(entry)} />
												</TableCell>
												<TableCell>
													<Fact view={buildDigestView(entry)} />
												</TableCell>
												<TableCell>
													<Fact view={generationView(entry)} />
												</TableCell>
												<TableCell>
													<Fact view={runningView(entry)} />
												</TableCell>
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
							{item.exposedTeams.length === 0 ? (
								<span className="text-sm text-muted-foreground">No team</span>
							) : (
								<div className="flex flex-wrap gap-1">
									{item.exposedTeams.map((team) => (
										<Badge
											key={team.grant?.id}
											variant="secondary"
											title={`${team.roleName} at ${team.grant?.scopePath}`}
										>
											{team.subjectLabel}
										</Badge>
									))}
								</div>
							)}
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
