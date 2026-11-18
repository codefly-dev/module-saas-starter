"use client";

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Button,
	Checkbox,
	Label,
	Spinner,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, type ReactNode, useState } from "react";
import { useAccessibleScopes } from "./queries.js";
import type {
	CollectionAccessView,
	CollectionGrantSubject,
	CollectionGrantView,
	DatasourceClient,
} from "./types.js";

export function CollectionReadBoundary({
	client,
	orgId,
	nodeId,
	children,
}: {
	client: DatasourceClient;
	orgId: string;
	nodeId: string;
	children: ReactNode;
}) {
	const scopes = useAccessibleScopes(client, orgId);
	// This gate decides whether a collection is shown at all, so its wait needs
	// the same 200/300 rule as any other: a cached answer resolves well inside
	// the delay, and "Checking collection permissions…" appearing and vanishing
	// inside it is the flicker the rule exists to remove.
	const { indicator, quiet } = useLoadingPhase(
		scopes.isPending && scopes.fetchStatus !== "idle",
	);
	if (!client.listAccessibleScopes || scopes.isError)
		return (
			<p role="alert">
				Couldn’t verify collection permissions. Retry when the permission
				service is available.
			</p>
		);
	if (quiet) return null;
	if (indicator)
		return <Spinner label="Checking collection permissions" size="sm" />;
	if (scopes.isPending) return null;
	if (
		!scopes.data?.some(
			(scope) => scope.nodeId === nodeId && scope.actions.includes("read"),
		)
	) {
		return (
			<p role="status">
				You don’t have read access to this collection. Ask an organization
				administrator for access.
			</p>
		);
	}
	return <Fragment key={`${orgId}:${nodeId}`}>{children}</Fragment>;
}

/**
 * BulkGrantOutcome is what one Grant press did, per subject.
 *
 * Granting is a loop of independent writes, so it has no single answer: one
 * subject may be refused while the rest are granted. Reporting only the first
 * failure would silently claim the others never happened, and reporting only
 * "failed" would send an administrator to re-grant subjects that already hold
 * the role.
 */
interface BulkGrantOutcome {
	granted: string[];
	failed: { label: string; message: string }[];
}

/**
 * The host refuses a grant with `mfa_required` when the administrator's second
 * factor was confirmed more than fifteen minutes ago; a refresh never renews
 * it. The sign-in page confirms the passkey and honours `next`, so the
 * administrator comes back here and grants again.
 */
function needsRecentFactor(message: string): boolean {
	return (
		message.includes("mfa_required") &&
		(message.includes("failed_precondition") ||
			message.includes("FailedPrecondition"))
	);
}

function stepUpHref(): string {
	const here =
		typeof window === "undefined"
			? "/"
			: `${window.location.pathname}${window.location.search}`;
	return `/auth/login?step_up=1&next=${encodeURIComponent(here)}`;
}

export function CollectionGrants({
	client,
	orgId,
	collection,
	connectedRepo,
	onDismiss,
}: {
	client: DatasourceClient;
	orgId: string;
	collection: CollectionAccessView;
	/**
	 * The repository just connected into this collection, when this section is
	 * being offered as part of the connect flow rather than opened from the
	 * source list. It changes only what the section says, never what it does.
	 */
	connectedRepo?: string | undefined;
	/** Present only in the connect flow, where the section can be put away. */
	onDismiss?: (() => void) | undefined;
}) {
	// A set, not a single value: granting one subject at a time is the whole of
	// what made this flow unreasonable — an administrator who has just connected
	// a repository for a team of eight repeated the same four steps eight times.
	const [selected, setSelected] = useState<string[]>([]);
	const [removing, setRemoving] = useState<CollectionGrantView | null>(null);
	const [outcome, setOutcome] = useState<BulkGrantOutcome | null>(null);
	const cache = useQueryClient();
	const subjects = useQuery({
		queryKey: ["collection-grant-subjects", orgId],
		queryFn: () => client.listGrantSubjects!(orgId),
		retry: false,
	});
	const refresh = () =>
		Promise.all([
			cache.invalidateQueries({ queryKey: ["collection-access", orgId] }),
			cache.invalidateQueries({ queryKey: ["datasource-boundaries", orgId] }),
		]);
	const revoke = useMutation({
		mutationFn: (grant: CollectionGrantView) =>
			client.revokeCollectionRead!(orgId, grant),
		onSuccess: async () => {
			await refresh();
			setRemoving(null);
		},
	});
	const grant = useMutation({
		// Sequential on purpose, not Promise.all. The host resolves the read role
		// for this deployment's content resource and mints it if it does not
		// exist yet, so concurrent grants would race to create one role whose
		// name is unique per organization — the first would win and the rest
		// would fail on a collision that says nothing about the grant.
		mutationFn: async (
			chosen: CollectionGrantSubject[],
		): Promise<BulkGrantOutcome> => {
			const result: BulkGrantOutcome = { granted: [], failed: [] };
			for (const subject of chosen) {
				try {
					await client.grantCollectionRead!(
						orgId,
						collection.scopePath,
						subject,
					);
					result.granted.push(subject.label);
				} catch (error) {
					result.failed.push({
						label: subject.label,
						message:
							error instanceof Error ? error.message : "unexpected error",
					});
				}
			}
			return result;
		},
		onSuccess: async (result) => {
			setOutcome(result);
			// Keep the ones that did not land selected, so the retry is the same
			// press rather than a re-pick from a list of forty names.
			setSelected((previous) =>
				previous.filter((key) =>
					result.failed.some(
						(failure) =>
							subjects.data?.find(
								(subject) => `${subject.kind}:${subject.id}` === key,
							)?.label === failure.label,
					),
				),
			);
			await refresh();
		},
	});
	const chosen = (subjects.data ?? []).filter((subject) =>
		selected.includes(`${subject.kind}:${subject.id}`),
	);
	const busy = grant.isPending || revoke.isPending;
	// The timing lives here, above every branch that consumes it. Mounting the
	// indicator inside the branch gated on the wait would keep the 200 ms delay
	// and lose the 300 ms floor — the floor is state inside the component the
	// answer unmounts, so a reply landing at 210 ms would show the indicator for
	// 10 ms, which is the blink the floor exists to prevent.
	//
	// `fetchStatus` is part of the condition because a query that is not fetching
	// is not a wait: a disabled query sits at isPending forever and must not earn
	// an indicator.
	const subjectsPhase = useLoadingPhase(
		subjects.isPending && subjects.fetchStatus !== "idle",
	);
	return (
		<section
			aria-label={`Who can read ${collection.label}`}
			className="space-y-3 rounded-lg border p-4"
		>
			<div className="flex items-start justify-between gap-3">
				<div className="space-y-1">
					<h4 className="type-label">Who can read {collection.label}</h4>
					<p className="type-caption-plain text-muted-foreground">
						{connectedRepo
							? `${connectedRepo} is connected. Connecting doesn't share anything; choose who can read it.`
							: "Only the people and teams listed here can read it. Platform administrators can read every collection."}
					</p>
				</div>
				{onDismiss && (
					<Button type="button" variant="ghost" size="sm" onClick={onDismiss}>
						{connectedRepo ? "Not now" : "Done"}
					</Button>
				)}
			</div>
			{collection.grants.length > 0 && (
				<ul aria-label="Readers" className="divide-y rounded-md border">
					{collection.grants.map((grant) => (
						<li
							key={grant.id}
							className="flex flex-wrap items-center justify-between gap-2 px-3 py-2"
						>
							<span className="type-body">
								{grant.subjectLabel}
								<span className="type-caption-plain text-muted-foreground">
									{" · "}
									{grant.subjectKind === "team" ? "Team" : "Person"}
									{grant.scopePath !== collection.scopePath &&
										" · from a wider grant"}
								</span>
							</span>
							<Button
								type="button"
								variant="ghost"
								size="sm"
								disabled={busy}
								aria-label={`Remove ${grant.subjectLabel}`}
								onClick={() => {
									revoke.reset();
									setRemoving(grant);
								}}
							>
								Remove
							</Button>
						</li>
					))}
				</ul>
			)}
			{subjects.isError ? (
				<p role="alert" className="type-body text-destructive">
					Couldn’t load this organization’s people and teams.
				</p>
			) : subjectsPhase.indicator ? (
				<Spinner label="Loading people and teams" />
			) : subjectsPhase.quiet || subjects.isPending ? null : subjects.data &&
				subjects.data.length === 0 ? (
				<p role="status" className="type-body text-muted-foreground">
					This organization has no people or teams to share it with yet.
				</p>
			) : (
				<div className="space-y-2">
					<fieldset disabled={busy} className="space-y-2">
						<legend className="type-label">Add people or teams</legend>
						<div className="max-h-48 space-y-2 overflow-y-auto rounded-lg border p-3">
							{subjects.data?.map((subject) => {
								const key = `${subject.kind}:${subject.id}`;
								return (
									<div key={key} className="flex items-center gap-2">
										<Checkbox
											id={`grant-${collection.nodeId}-${key}`}
											checked={selected.includes(key)}
											onCheckedChange={(checked) => {
												setOutcome(null);
												setSelected((current) =>
													checked
														? [...current, key]
														: current.filter((value) => value !== key),
												);
											}}
										/>
										<Label htmlFor={`grant-${collection.nodeId}-${key}`}>
											{subject.label}
										</Label>
										<span className="type-caption-plain text-muted-foreground">
											{subject.kind === "team" ? "Team" : "Person"}
										</span>
									</div>
								);
							})}
						</div>
					</fieldset>
					<Button
						type="button"
						size="sm"
						disabled={chosen.length === 0 || busy}
						aria-busy={grant.isPending}
						onClick={() => {
							setOutcome(null);
							grant.mutate(chosen);
						}}
					>
						{chosen.length > 1
							? `Let ${chosen.length} read it`
							: "Let them read it"}
					</Button>
				</div>
			)}
			<AlertDialog
				open={!!removing}
				onOpenChange={(open) => {
					if (!open && !revoke.isPending) setRemoving(null);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							Remove access for {removing?.subjectLabel}?
						</AlertDialogTitle>
						<AlertDialogDescription>
							{removing?.subjectLabel} will lose this grant to read{" "}
							{collection.label}.
							{removing &&
								removing.scopePath !== collection.scopePath &&
								" This grant also covers other collections. Removing it affects all of them."}
						</AlertDialogDescription>
					</AlertDialogHeader>
					{revoke.isError && (
						<p role="alert" className="type-body text-destructive">
							Couldn’t remove access. Ask your organization administrator to
							check your access.
						</p>
					)}
					<AlertDialogFooter>
						<AlertDialogCancel disabled={revoke.isPending}>
							Cancel
						</AlertDialogCancel>
						<AlertDialogAction
							variant="destructive"
							disabled={revoke.isPending}
							onClick={(event) => {
								event.preventDefault();
								if (removing) revoke.mutate(removing);
							}}
						>
							{revoke.isPending ? "Removing…" : "Remove access"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
			{/* Per subject, because the loop's outcome is per subject: a run that
			    granted six of eight is neither a success nor a failure. */}
			{outcome && outcome.granted.length > 0 && (
				<p role="status" className="type-body">
					{outcome.granted.join(", ")} can now read {collection.label}.
				</p>
			)}
			{outcome?.failed.map((failure) => (
				<p
					role="alert"
					key={failure.label}
					className="type-body text-destructive"
				>
					{needsRecentFactor(failure.message) ? (
						<>
							Couldn’t share with {failure.label}: confirm your passkey (it
							lasts 15 minutes), then try again.{" "}
							<a href={stepUpHref()} className="underline">
								Confirm passkey
							</a>
						</>
					) : (
						<>
							Couldn’t share with {failure.label}. Ask your organization
							administrator to check your access.
						</>
					)}
				</p>
			))}
			{grant.isError && (
				<p role="alert" className="type-body text-destructive">
					Couldn’t change access. Ask your organization administrator to check
					your access.
				</p>
			)}
		</section>
	);
}
