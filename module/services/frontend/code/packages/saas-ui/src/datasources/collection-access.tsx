"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useState, type ReactNode } from "react";
import { useAccessibleScopes } from "./queries.js";
import type {
	CollectionAccessView,
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
	if (!client.listAccessibleScopes || scopes.isError)
		return (
			<p role="alert">
				Couldn’t verify collection permissions. Retry when the permission
				service is available.
			</p>
		);
	if (scopes.isPending)
		return <p role="status">Checking collection permissions…</p>;
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

export function CollectionGrants({
	client,
	orgId,
	collection,
}: {
	client: DatasourceClient;
	orgId: string;
	collection: CollectionAccessView;
}) {
	const [subjectKey, setSubjectKey] = useState("");
	const cache = useQueryClient();
	const subjects = useQuery({
		queryKey: ["collection-grant-subjects", orgId],
		queryFn: () => client.listGrantSubjects!(orgId),
		retry: false,
	});
	const change = useMutation({
		mutationFn: async (grant?: CollectionGrantView) => {
			if (grant) await client.revokeCollectionRead!(orgId, grant);
			else {
				const subject = subjects.data!.find(
					(subject) => `${subject.kind}:${subject.id}` === subjectKey,
				)!;
				await client.grantCollectionRead!(orgId, collection.scopePath, subject);
			}
		},
		onSuccess: async () => {
			await Promise.all([
				cache.invalidateQueries({ queryKey: ["collection-access", orgId] }),
				cache.invalidateQueries({ queryKey: ["datasource-boundaries", orgId] }),
			]);
		},
	});
	return (
		<section
			aria-label={`Read grants for ${collection.label}`}
			className="space-y-3 rounded border p-4"
		>
			<h3 className="font-medium">Who can read {collection.label}</h3>
			<p className="text-sm">
				Connecting a source grants no access to its creator or a default team.
				An administrator explicitly grants documents/read to a member or team.
			</p>
			{collection.grants.length === 0 ? (
				<p>
					No collection read grants. Ingestion can proceed, but viewers need a
					grant to read documents.
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
								disabled={change.isPending}
								onClick={() => {
									if (
										window.confirm(
											`Revoke ${grant.roleName} from ${grant.subjectLabel}? This removes every permission in this role at ${grant.scopePath} and its descendants.`,
										)
									)
										change.mutate(grant);
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
			) : (
				<>
					<label>
						Grant read access to{" "}
						<select
							aria-label="Grant read access to"
							value={subjectKey}
							onChange={(event) => setSubjectKey(event.target.value)}
						>
							<option value="">Choose a member or team</option>
							{subjects.data?.map((subject) => (
								<option
									key={`${subject.kind}:${subject.id}`}
									value={`${subject.kind}:${subject.id}`}
								>
									{subject.label}
								</option>
							))}
						</select>
					</label>
					<button
						type="button"
						disabled={!subjectKey || change.isPending}
						onClick={() => change.mutate(undefined)}
					>
						Grant read access
					</button>
				</>
			)}
			{change.isError && (
				<p role="alert">
					Couldn’t change collection grants: {change.error.message}
				</p>
			)}
		</section>
	);
}
