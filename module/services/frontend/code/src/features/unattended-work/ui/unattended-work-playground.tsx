"use client";

import { KeyRound, RefreshCw } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	DelayedLoading,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	useLoadingPhase,
} from "@/shared/ui";
import { sourceLabel } from "../model/source-label";
import {
	useDelegateSourceTriggers,
	useReconcileSource,
	useRevokeTriggerDelegation,
} from "../service/mutations";
import {
	useSourceTriggerDeclaration,
	useTriggerSources,
} from "../service/queries";
import { declaredIdentity, Fact, instant } from "./source-trigger-schedules";

/**
 * The unattended-work playground: the whole of this module's answer to "whose
 * authority does work no person started run under?", in one place a person can
 * drive.
 *
 * The walk it exists for: mint a delegation reference for an installed
 * release's triggers, watch the child Work Contexts minted against it as
 * occurrences run, revoke it, and watch the next occurrence refused. Each step
 * is a host operation and none of it is a simulation — "run one occurrence" is
 * the same host operation a declared schedule's occurrence admits, with an
 * effect identity this press chose.
 *
 * What the playground cannot show is a DECLARED schedule firing, and that is
 * the honest state of the contract rather than a gap in the page: the clock
 * belongs to the module that executes declared work, and nothing in this
 * repository composes one. Because nothing does, this host fires the reconcile
 * schedule itself as a labelled stopgap (issue #1112) — so a source does
 * refresh on its interval, just not through the declaration this page shows.
 * The schedules panel says which is which.
 */
export function UnattendedWorkPlayground() {
	const { organizationId: orgId = "" } = useAuth();
	const [selected, setSelected] = useState("");
	const sources = useTriggerSources(orgId);
	const outcome = readOutcome(sources.isError, sources.error);
	const rows = mayKeepRetainedRows(outcome) ? (sources.data ?? []) : [];
	const stale = staleReadNotice(outcome, "this organization's data sources");
	const waiting = sources.isPending && sources.fetchStatus !== "idle";
	const sourceId = selected || rows[0]?.id || "";
	const selectedSource = rows.find((source) => source.id === sourceId);
	const selectedLabel = selectedSource ? sourceLabel(selectedSource) : "";

	return (
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 data-slot="page-title" className="type-page-title">
						Unattended work
					</h1>
					<p className="text-muted-foreground">
						Work no person started still carries a signed identity. A release
						declares whose, and this host re-derives it from current facts on
						every occurrence.
					</p>
				</div>
				<OrgSelector />
			</div>

			{stale && (
				<p role="status" className="text-sm text-muted-foreground">
					{stale}
				</p>
			)}

			{waiting ? (
				// The kit's never-flash wait: nothing for the first 200ms, then an
				// indicator that stays long enough to read.
				<DelayedLoading active label="Reading this organization's sources" />
			) : rows.length === 0 ? (
				outcome === "empty" ? (
					<EmptyState
						icon={KeyRound}
						title="Connect a data source to bind a trigger identity"
						description="A delegation reference is bound to a connected source: that is what gives an occurrence a person's authority to borrow."
					/>
				) : (
					<p
						role="status"
						className="py-6 text-center text-sm text-muted-foreground"
					>
						{readOutcomeMessage(
							outcome,
							"this organization's data sources",
							"",
						)}
					</p>
				)
			) : (
				<>
					<Card>
						<CardHeader>
							<CardTitle>The source a release&apos;s triggers name</CardTitle>
							<CardDescription>
								A trigger names a reference, and the reference is the source.
								The host resolves whichever module&apos;s live delegation is
								active on it at every mint. That also means revoking one does
								not stop an installed trigger for good: a reconnect records a
								new delegation and the same reference resolves again, so a
								revocation must be followed by not reconnecting, or by removing
								the trigger. A reference that revocation ends permanently needs
								an opaque, delegation-level reference the firing module can
								name.
							</CardDescription>
						</CardHeader>
						<CardContent>
							<Select
								value={sourceId}
								onValueChange={(value) => setSelected(value ?? "")}
							>
								<SelectTrigger aria-label="Data source" className="w-96">
									{/* The label is supplied here rather than left to the
									    popup: a trigger that read its own value would show a
									    raw source id until the popup mounted. */}
									<SelectValue placeholder="Select a source">
										{selectedLabel}
									</SelectValue>
								</SelectTrigger>
								<SelectContent>
									{rows.map((source) => (
										<SelectItem key={source.id} value={source.id}>
											{sourceLabel(source)}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</CardContent>
					</Card>
					{sourceId && (
						<TriggerIdentityPanel orgId={orgId} sourceId={sourceId} />
					)}
				</>
			)}
		</div>
	);
}

function TriggerIdentityPanel({
	orgId,
	sourceId,
}: {
	orgId: string;
	sourceId: string;
}) {
	const read = useSourceTriggerDeclaration(orgId, sourceId);
	const delegate = useDelegateSourceTriggers(orgId);
	const revoke = useRevokeTriggerDelegation(orgId);
	const reconcile = useReconcileSource(orgId);

	const outcome = readOutcome(read.isError, read.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const declaration = withheld ? undefined : read.data?.declaration;
	// Only meaningful when an answer survived: a refusal has nothing to be
	// stale about, having just been deleted at the query boundary.
	const declarationStale = declaration
		? staleReadNotice(outcome, "this trigger")
		: null;
	// The same never-flashing wait the schedule row uses, so the pending window
	// is a state of its own rather than the tail of the empty one — and on the
	// same model as the source list beside it: pending with a non-idle fetch
	// status is an answer still coming, whether a request is on the wire or
	// PAUSED because the browser is offline. `isFetching` is false while paused,
	// so a gate on it called a paused read finished (issue #1121).
	const unanswered = read.isPending && read.fetchStatus !== "idle";
	const declarationPhase = useLoadingPhase(unanswered);
	const waiting =
		unanswered || declarationPhase.indicator || declarationPhase.quiet;
	const references = withheld ? [] : (read.data?.delegations ?? []);
	const children = withheld ? [] : (read.data?.children ?? []);
	const live = references.filter((reference) => reference.active);

	return (
		<div className="space-y-6">
			<Card>
				<CardHeader>
					<CardTitle>The declared trigger</CardTitle>
					<CardDescription>
						The source&apos;s reconcile interval is the configuration; this is
						the trigger it declares. Nothing fires this declaration yet, so the
						host still runs the schedule itself as a stopgap.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-2 text-sm" data-slot="declared-trigger">
					{/*
					 * THE CARD SAYS WHICH STATE THE READ IS IN (issue #1121, r22
					 * F1). It withheld refused rows already, which is right, and
					 * then rendered every fact as an em dash — which reads as
					 * "this host declares nothing" to a viewer who is simply not
					 * allowed to see it. A refusal and a failure are now said in
					 * the card they are about; a transient failure keeps the
					 * prior answer under a stale notice.
					 */}
					<DelayedLoading
						active={unanswered}
						label="Reading the declared trigger"
					/>
					{declarationStale && (
						<p role="status" className="text-muted-foreground">
							{declarationStale}
						</p>
					)}
					{/*
					 * THE FACTS NEED A DECLARATION — the branch is on having
					 * one, never on the read having finished. Testing
					 * `!declaration && !read.isFetching` meant the pending
					 * window fell through to the facts list, which rendered
					 * dashes and an Identity row computed from no declaration
					 * at all: "None — occurrences would be refused", the exact
					 * false claim about authority the schedule row was fixed
					 * for (issue #1121).
					 */}
					{declaration ? null : waiting ? null : (
						<p role="status" className="text-muted-foreground">
							{readOutcomeMessage(
								outcome,
								"this source's declared trigger",
								"This source declares no trigger.",
							)}
						</p>
					)}
					{!declaration ? null : declaration.undeclarableReason ? (
						<p role="status" className="text-muted-foreground">
							{declaration.undeclarableReason}
						</p>
					) : (
						<dl className="grid gap-2 sm:grid-cols-2">
							<Fact label="Release" value={declaration.release || "—"} mono />
							<Fact
								label="Operation"
								value={declaration.operation || "—"}
								mono
							/>
							<Fact
								label="Schedule"
								value={declaration.schedule?.cron ?? "—"}
								mono
							/>
							<Fact
								label="Timezone"
								value={declaration.schedule?.timezone ?? "—"}
							/>
							<Fact
								label="Missed occurrences"
								value={declaration.schedule?.missedRun ?? "—"}
							/>
							<Fact
								label="Overlap"
								value={declaration.schedule?.overlap ?? "—"}
							/>
							<Fact
								label="Occurrence deadline"
								value={nanosecondsAsDuration(declaration.schedule?.deadline)}
							/>
							<Fact
								label="Occurrence input"
								value={declaration.schedule?.inputJson || "—"}
								mono
							/>
							<Fact label="Identity" value={declaredIdentity(declaration)} />
							<Fact
								label="Declaration"
								value={declaration.digest || "—"}
								mono
							/>
						</dl>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader className="flex flex-row items-start justify-between gap-3">
					<div>
						<CardTitle>Delegation references</CardTitle>
						<CardDescription>
							What an occurrence borrows: the organization, the person and
							exactly the scopes the binding declares. Revoking one refuses the
							next occurrence; work already in flight is not recalled.
						</CardDescription>
					</div>
					<Button
						onClick={() =>
							delegate.mutate(sourceId, {
								onSuccess: (result) =>
									toast.success(
										result.delegations.length > 0
											? "Delegation reference minted"
											: "No composed module accepts a delegation here",
									),
								onError: (error) => toast.error(String(error)),
							})
						}
						disabled={delegate.isPending}
						data-slot="mint-reference"
					>
						<KeyRound className="h-4 w-4" />
						Mint a reference
					</Button>
				</CardHeader>
				<CardContent>
					{references.length === 0 ? (
						<p
							role="status"
							className="py-6 text-center text-sm text-muted-foreground"
						>
							{outcome === "empty"
								? "No reference is bound to this source's triggers. An occurrence would be refused."
								: readOutcomeMessage(outcome, "this source's delegations", "")}
						</p>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Module and binding</TableHead>
									<TableHead>Owner</TableHead>
									<TableHead>Scopes</TableHead>
									<TableHead>State</TableHead>
									<TableHead />
								</TableRow>
							</TableHeader>
							<TableBody>
								{references.map((reference) => (
									<TableRow key={reference.delegationId} data-slot="reference">
										<TableCell className="font-mono text-xs">
											{reference.module}/{reference.binding}
										</TableCell>
										<TableCell className="font-mono text-xs">
											{reference.ownerPrincipalId}
										</TableCell>
										<TableCell className="font-mono text-xs">
											{reference.scopes.join(", ") || "—"}
										</TableCell>
										<TableCell>
											{reference.active ? (
												<Badge tone="success" dot>
													Live
												</Badge>
											) : (
												<Badge tone="neutral">Revoked</Badge>
											)}
										</TableCell>
										<TableCell className="text-right">
											{reference.active && (
												<Button
													variant="outline"
													size="sm"
													data-slot="revoke-reference"
													onClick={() =>
														revoke.mutate(
															{
																sourceId,
																delegationId: reference.delegationId,
															},
															{
																onSuccess: () =>
																	toast.success("Reference revoked"),
																onError: (error) => toast.error(String(error)),
															},
														)
													}
													disabled={revoke.isPending}
												>
													Revoke
												</Button>
											)}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader className="flex flex-row items-start justify-between gap-3">
					<div>
						<CardTitle>Occurrences and the children they minted</CardTitle>
						<CardDescription>
							Each occurrence mints its own child, lives about a minute and
							caches nothing. This is the only record of one — so a revocation
							shows up as the next occurrence having no child here at all.
						</CardDescription>
					</div>
					<Button
						variant="outline"
						data-slot="run-occurrence"
						onClick={() =>
							reconcile.mutate(sourceId, {
								onSuccess: (result) =>
									toast.success("Occurrence admitted", {
										description: `Job ${result.jobId}`,
									}),
								onError: (error) => toast.error(String(error)),
							})
						}
						// The DISABLED state is unchanged: refusing to act on a
						// reference set this page cannot see is correct, and this
						// change is not about what the button does. What changes is
						// what the operator is TOLD — "No live reference" is a claim
						// about the host's answer, and a read that has not answered
						// has made none (issue #1121, r24).
						disabled={reconcile.isPending || live.length === 0}
						title={
							live.length === 0
								? unanswered
									? "Still reading this source’s references"
									: !read.isSuccess
										? readOutcomeMessage(
												outcome,
												"this source’s references",
												"No live reference: an occurrence would be refused",
											)
										: "No live reference: an occurrence would be refused"
								: undefined
						}
					>
						<RefreshCw className="h-4 w-4" />
						Run one occurrence
					</Button>
				</CardHeader>
				<CardContent>
					{/*
					 * A TRUE EMPTY IS ONLY KNOWABLE AFTER A SUCCESSFUL READ. This
					 * card reads the same response as the declaration above it, and
					 * reported "no child has been minted" off an array length — so a
					 * held request, a paused read and a refusal all produced a
					 * statement about this source's children that the host had never
					 * made (issue #1121, r24). The empty sentence now waits for the
					 * read that licenses it; everything else says which state it is
					 * in. A transient failure keeps the rows, because those children
					 * were really minted and one request not coming back does not
					 * unmint them.
					 */}
					<DelayedLoading
						active={unanswered}
						label="Reading the children this source minted"
					/>
					{children.length > 0 ? (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Minted</TableHead>
									<TableHead>Audience</TableHead>
									<TableHead>Scopes</TableHead>
									<TableHead>Reach</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{children.map((child) => (
									<TableRow key={child.record} data-slot="child-context">
										<TableCell className="text-sm">
											{instant(child.mintedAt)}
										</TableCell>
										<TableCell className="font-mono text-xs">
											{child.audience}
										</TableCell>
										<TableCell className="font-mono text-xs">
											{child.scopes.join(", ") || "—"}
										</TableCell>
										<TableCell className="text-sm">
											{child.lookup
												? "Receipt lookup only"
												: "Produces the effect"}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					) : unanswered ? null : read.isSuccess ? (
						<p
							role="status"
							className="py-6 text-center text-sm text-muted-foreground"
						>
							No child has been minted against this source&apos;s references.
						</p>
					) : (
						<p
							role="status"
							className="py-6 text-center text-sm text-muted-foreground"
						>
							{readOutcomeMessage(
								outcome,
								"the children this source minted",
								"No child has been minted against this source’s references.",
							)}
						</p>
					)}
				</CardContent>
			</Card>
		</div>
	);
}

/**
 * One occurrence's deadline, read for a person. The declaration carries
 * nanoseconds because that is what the installation decode accepts; nobody
 * reads nanoseconds.
 */
function nanosecondsAsDuration(nanos?: bigint): string {
	// BigInt literals need ES2020 and this project targets lower, so the
	// divisor is built rather than written as 1_000_000_000n.
	if (nanos === undefined || nanos === BigInt(0)) return "—";
	const seconds = Number(nanos / BigInt(1_000_000_000));
	if (seconds % 60 === 0) return `${seconds / 60}m`;
	return `${seconds}s`;
}
