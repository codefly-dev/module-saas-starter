"use client";

import { Spinner, useLoadingPhase } from "@codefly-dev/ui/layout";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, type ReactNode, useState } from "react";
import { useAccessibleScopes } from "./queries.js";
import type {
	CollectionAccessView,
	CollectionGrantView,
	DatasourceClient,
	CollectionGrantSubject,
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
		onSuccess: refresh,
	});
	const grant = useMutation({
		// Sequential on purpose, not Promise.all. The host resolves the read role
		// for this deployment's content resource and mints it if it does not
		// exist yet, so concurrent grants would race to create one role whose
		// name is unique per organization — the first would win and the rest
		// would fail on a collision that says nothing about the grant.
		mutationFn: async (chosen: CollectionGrantSubject[]): Promise<BulkGrantOutcome> => {
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
			aria-label={`Read grants for ${collection.label}`}
			className="space-y-3 rounded border p-4"
		>
			<div className="flex items-start justify-between gap-3">
				<h3 className="font-medium">Who can read {collection.label}</h3>
				{onDismiss && (
					<button type="button" onClick={onDismiss}>
						Not now
					</button>
				)}
			</div>
			{connectedRepo ? (
				<p className="text-sm">
					{connectedRepo} is connected and syncing into {collection.label}.
					Nobody can read it yet — connecting grants no access, not even to
					you. Grant the members and teams who need it now, or later from
					Manage read grants on the source.
				</p>
			) : (
				<p className="text-sm">
					Connecting a source grants no access to its creator or a default team.
					An administrator explicitly grants read access to a member or team.
					Platform administrators read every collection without a grant.
				</p>
			)}
			{collection.grants.length === 0 ? (
				<p>
					No collection read grants. Ingestion can proceed, but viewers other
					than platform administrators need a grant to read this collection.
				</p>
			) : (
				<ul>
					{collection.grants.map((grant) => (
						<li key={grant.id} className="space-x-2">
							<span>
								{grant.subjectLabel} ({grant.subjectKind}) · {grant.roleName} ·
								Granted by {grant.actorLabel}
							</span>
							{grant.scopePath !== collection.scopePath && (
								<span>Inherited from {grant.scopePath}</span>
							)}
							<button
								type="button"
								disabled={busy}
								onClick={() => {
									if (
										window.confirm(
											`Revoke ${grant.roleName} from ${grant.subjectLabel}? This removes every permission in this role at ${grant.scopePath} and its descendants.`,
										)
									)
										revoke.mutate(grant);
								}}
							>
								Revoke grant
							</button>
						</li>
					))}
				</ul>
			)}
			{subjects.isError ? (
				<p role="alert">Couldn’t load members and teams.</p>
			) : subjectsPhase.indicator ? (
				<Spinner label="Loading members and teams…" />
			) : subjectsPhase.quiet || subjects.isPending ? (
				// The read is outstanding and inside the pre-delay window: render
				// nothing at all rather than falling through to the branches below,
				// either of which would answer a question nobody has an answer to
				// yet — "nobody to grant to", or a picker with no names in it.
				null
			) : subjects.data && subjects.data.length === 0 ? (
				<p role="status">
					This organization has no members or teams to grant read access to.
				</p>
			) : (
				<>
					<label htmlFor={`grant-subjects-${collection.nodeId}`}>
						Grant read access to
					</label>
					{/* A native multiple select: granting is one press for however many
					    subjects were picked, rather than the same four steps repeated
					    once per person. */}
					<select
						id={`grant-subjects-${collection.nodeId}`}
						aria-label="Grant read access to"
						multiple
						size={Math.min(8, Math.max(3, subjects.data?.length ?? 3))}
						value={selected}
						onChange={(event) => {
							setOutcome(null);
							setSelected(
								Array.from(event.target.selectedOptions, (o) => o.value),
							);
						}}
					>
						{subjects.data?.map((subject) => (
							<option
								key={`${subject.kind}:${subject.id}`}
								value={`${subject.kind}:${subject.id}`}
							>
								{subject.label}
							</option>
						))}
					</select>
					<button
						type="button"
						disabled={chosen.length === 0 || busy}
						aria-busy={grant.isPending}
						onClick={() => {
							setOutcome(null);
							grant.mutate(chosen);
						}}
					>
						{chosen.length > 1
							? `Grant read access to ${chosen.length} subjects`
							: "Grant read access"}
					</button>
				</>
			)}
			{/* Per subject, because the loop's outcome is per subject: a run that
			    granted six of eight is neither a success nor a failure, and saying
			    only one of those sends an administrator to redo work that landed or
			    to walk away from work that did not. */}
			{outcome && outcome.granted.length > 0 && (
				<p role="status">
					Granted read access to {outcome.granted.join(", ")}.
				</p>
			)}
			{outcome?.failed.map((failure) => (
				<p role="alert" key={failure.label}>
					Couldn’t grant read access to {failure.label}: {failure.message}
				</p>
			))}
			{grant.isError && (
				<p role="alert">
					Couldn’t change collection grants: {grant.error.message}
				</p>
			)}
			{revoke.isError && (
				<p role="alert">
					Couldn’t change collection grants: {revoke.error.message}
				</p>
			)}
		</section>
	);
}
