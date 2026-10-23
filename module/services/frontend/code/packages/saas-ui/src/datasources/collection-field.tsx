"use client";

import { Input, Label } from "@codefly-dev/ui/layout";
import type { UseFormRegisterReturn } from "react-hook-form";
import { fieldErrorClass as errorClass } from "./credential-mode.js";
import type { CollectionAccessView } from "./types.js";

interface CollectionFieldProps {
	idFor: (name: string) => string;
	collections?: CollectionAccessView[];
	readableNodeIds?: string[];
	collectionError?: boolean;
	boundaryNodeId?: string;
	targetCollection?: string;
	/** An existing collection was picked, or "" to create one. */
	onSelect: (nodeId: string, label: string) => void;
	inputProps: UseFormRegisterReturn;
	error?: string;
}

/**
 * Where a new source's content lands: an existing collection, or a label to
 * create one under. Every connect form shares it, so who can read the result
 * is told the same way whichever provider is being connected.
 */
export function CollectionField({
	idFor,
	collections,
	readableNodeIds,
	collectionError,
	boundaryNodeId,
	targetCollection,
	onSelect,
	inputProps,
	error,
}: CollectionFieldProps) {
	const selected = collections?.find(
		(collection) => collection.nodeId === boundaryNodeId,
	);
	const named =
		selected ??
		collections?.find((collection) => collection.label === targetCollection);
	return (
		<div className="space-y-2">
			{collections && (
				<>
					<Label htmlFor={idFor("existing")}>Existing collection</Label>
					<select
						id={idFor("existing")}
						aria-label="Existing collection"
						value={boundaryNodeId ?? ""}
						onChange={(event) =>
							onSelect(
								event.target.value,
								collections.find(
									(collection) => collection.nodeId === event.target.value,
								)?.label ?? "",
							)
						}
					>
						<option value="">Create a collection</option>
						{collections.map((collection) => (
							<option key={collection.nodeId} value={collection.nodeId}>
								{collection.label}
							</option>
						))}
					</select>
				</>
			)}
			<Label htmlFor={idFor("collection")}>Target collection</Label>
			<Input
				id={idFor("collection")}
				readOnly={!!selected}
				aria-invalid={!!error}
				aria-describedby={error ? idFor("collection-error") : undefined}
				placeholder="Documents-store collection to land entries in"
				{...inputProps}
			/>
			<p className="type-body">
				Connecting grants no read access to the creator or a default team.
				Organization administrators explicitly grant members or teams read
				access. Platform administrators read every collection without a grant.
			</p>
			{collectionError ? (
				<p role="alert">Couldn’t inspect collection grants.</p>
			) : named ? (
				<p>
					Readers:{" "}
					{named.grants.map((grant) => grant.subjectLabel).join(", ") ||
						"No collection read grants"}
					.{" "}
					{readableNodeIds === undefined
						? "Your read permission is unresolved."
						: readableNodeIds.includes(named.nodeId)
							? "You can read this collection."
							: "You do not have read access. Ask an administrator for access."}
				</p>
			) : (
				<p>
					{collections
						? "New collections have no read grants. Your read permission must be checked after creation."
						: "Collection grants are unresolved. Inspect them in the host after connecting."}
				</p>
			)}
			{error && (
				<p id={idFor("collection-error")} className={errorClass}>
					{error}
				</p>
			)}
		</div>
	);
}
