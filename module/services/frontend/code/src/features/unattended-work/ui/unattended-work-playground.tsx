"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { KeyRound, RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import { UserLabel } from "@/components/user-label";
import type {
	TriggerChildContext,
	TriggerDelegationReference,
} from "@/gen/saas/accounts/v1/datasource_pb";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
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
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	DelayedLoading,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
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
import { declaredIdentity, Fact, instant } from "./declaration-facts";

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
		<PageBody>
			<PageHeader
				title="Unattended work"
				description="Work no person started still carries a signed identity. A release declares whose, and this host re-derives it from current facts on every occurrence."
				actions={<OrgSelector />}
			/>

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
						role="alert"
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
								<SelectTrigger
									aria-label="Data source"
									className="w-full max-w-96"
								>
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
		</PageBody>
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
	const [revokeTarget, setRevokeTarget] =
		useState<TriggerDelegationReference | null>(null);
	const reconcile = useReconcileSource(orgId);

	const outcome = readOutcome(read.isError, read.error);
	const withheld = !mayKeepRetainedRows(outcome);
	const declaration = withheld ? undefined : read.data?.declaration;
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
				<CardContent className="space-y-2 text-sm">
					{declaration?.undeclarableReason ? (
						<p role="status" className="text-muted-foreground">
							{declaration.undeclarableReason}
						</p>
					) : (
						<dl className="grid gap-2 sm:grid-cols-2">
							<Fact label="Release" value={declaration?.release ?? "—"} mono />
							<Fact
								label="Operation"
								value={declaration?.operation ?? "—"}
								mono
							/>
							<Fact
								label="Schedule"
								value={declaration?.schedule?.cron ?? "—"}
								mono
							/>
							<Fact
								label="Timezone"
								value={declaration?.schedule?.timezone ?? "—"}
							/>
							<Fact
								label="Missed occurrences"
								value={declaration?.schedule?.missedRun ?? "—"}
							/>
							<Fact
								label="Overlap"
								value={declaration?.schedule?.overlap ?? "—"}
							/>
							<Fact
								label="Occurrence deadline"
								value={nanosecondsAsDuration(declaration?.schedule?.deadline)}
							/>
							<Fact
								label="Occurrence input"
								value={declaration?.schedule?.inputJson || "—"}
								mono
							/>
							<Fact label="Identity" value={declaredIdentity(declaration)} />
							<Fact
								label="Declaration"
								value={declaration?.digest ?? "—"}
								mono
							/>
						</dl>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader className="flex flex-col items-start justify-between gap-3 sm:flex-row">
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
								onError: (error) =>
									toast.error(failureText("mint a reference", error)),
							})
						}
						disabled={delegate.isPending || read.isPending || read.isError}
						data-slot="mint-reference"
					>
						<KeyRound className="h-4 w-4" />
						Mint a reference
					</Button>
				</CardHeader>
				<CardContent>
					{read.isPending ? (
						<DelayedLoading active label="Reading background access" />
					) : read.isError ? (
						<ErrorState
							title="Couldn’t load background access"
							detail={readOutcomeMessage(
								outcome,
								"this source’s background access",
								"",
							)}
						/>
					) : references.length === 0 ? (
						<p
							role={outcome === "empty" ? "status" : "alert"}
							className="py-6 text-center text-sm text-muted-foreground"
						>
							{outcome === "empty"
								? "No reference is bound to this source's triggers. An occurrence would be refused."
								: readOutcomeMessage(outcome, "this source's delegations", "")}
						</p>
					) : (
						<ReferencesTable
							rows={references}
							onRevoke={setRevokeTarget}
							busy={revoke.isPending}
						/>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader className="flex flex-col items-start justify-between gap-3 sm:flex-row">
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
								onError: (error) =>
									toast.error(failureText("run an occurrence", error)),
							})
						}
						disabled={reconcile.isPending || live.length === 0}
						title={
							live.length === 0
								? "No live reference: an occurrence would be refused"
								: undefined
						}
					>
						<RefreshCw className="h-4 w-4" />
						Run one occurrence
					</Button>
				</CardHeader>
				<CardContent>
					{read.isPending ? (
						<DelayedLoading active label="Reading background runs" />
					) : read.isError ? (
						<ErrorState
							title="Couldn’t load background runs"
							detail={readOutcomeMessage(
								outcome,
								"this source’s background runs",
								"",
							)}
						/>
					) : children.length === 0 ? (
						<p
							role={outcome === "empty" ? "status" : "alert"}
							className="py-6 text-center text-sm text-muted-foreground"
						>
							{readOutcomeMessage(
								outcome,
								"what this source's occurrences minted",
								"No child has been minted against this source's references.",
							)}
						</p>
					) : (
						<ChildrenTable rows={children} />
					)}
				</CardContent>
			</Card>
			<AlertDialog
				open={revokeTarget !== null}
				onOpenChange={(open) => {
					if (!open && !revoke.isPending) setRevokeTarget(null);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Remove background access?</AlertDialogTitle>
						<AlertDialogDescription>
							Remove the access used by {revokeTarget?.module} for this data
							source? Future work using this access will be refused. Work
							already running is not stopped. Reconnecting the source can grant
							access again.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel disabled={revoke.isPending}>
							Keep access
						</AlertDialogCancel>
						<AlertDialogAction
							disabled={revoke.isPending}
							onClick={(event) => {
								event.preventDefault();
								if (!revokeTarget) return;
								revoke.mutate(
									{ sourceId, delegationId: revokeTarget.delegationId },
									{
										onSuccess: () => {
											toast.success("Background access removed");
											setRevokeTarget(null);
										},
										onError: (error) =>
											toast.error(
												failureText("remove background access", error),
											),
									},
								);
							}}
						>
							{revoke.isPending ? "Removing…" : "Remove access"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}

function ReferencesTable({
	rows,
	onRevoke,
	busy,
}: {
	rows: TriggerDelegationReference[];
	onRevoke: (row: TriggerDelegationReference) => void;
	busy: boolean;
}) {
	const columns = useMemo(() => {
		const col = createColumnHelper<TriggerDelegationReference>();
		return [
			col.accessor("module", { header: "Application" }),
			col.accessor("ownerPrincipalId", {
				header: "Person",
				cell: ({ getValue }) => <UserLabel userId={getValue()} />,
			}),
			col.accessor("scopes", {
				header: "Access",
				cell: ({ getValue }) =>
					`${getValue().length} allowed ${getValue().length === 1 ? "activity" : "activities"}`,
			}),
			col.accessor("active", {
				header: "State",
				cell: ({ getValue }) =>
					getValue() ? (
						<Badge tone="success" dot>
							Live
						</Badge>
					) : (
						<Badge tone="neutral">Revoked</Badge>
					),
			}),
			col.display({
				id: "actions",
				header: "Actions",
				cell: ({ row }) =>
					row.original.active ? (
						<Button
							variant="outline"
							size="sm"
							disabled={busy}
							onClick={() => onRevoke(row.original)}
						>
							Revoke
						</Button>
					) : null,
			}),
		];
	}, [onRevoke, busy]);
	const table = useReactTable({
		data: rows,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} />;
}

function ChildrenTable({ rows }: { rows: TriggerChildContext[] }) {
	const columns = useMemo(() => {
		const col = createColumnHelper<TriggerChildContext>();
		return [
			col.accessor("mintedAt", {
				header: "Started",
				cell: ({ getValue }) => instant(getValue()),
			}),
			col.accessor("audience", { header: "Application" }),
			col.accessor("scopes", {
				header: "Access",
				cell: ({ getValue }) =>
					`${getValue().length} allowed ${getValue().length === 1 ? "activity" : "activities"}`,
			}),
			col.accessor("lookup", {
				header: "Purpose",
				cell: ({ getValue }) =>
					getValue() ? "Receipt lookup only" : "Produces the effect",
			}),
		];
	}, []);
	const table = useReactTable({
		data: rows,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} />;
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
