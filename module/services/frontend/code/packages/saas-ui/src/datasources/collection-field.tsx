"use client";

import {
	Input,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@codefly-dev/ui/layout";
import type { ComponentProps } from "react";
import { fieldErrorClass as errorClass } from "./credential-mode.js";
import type { CollectionAccessView } from "./types.js";

interface CollectionFieldProps {
	idFor: (name: string) => string;
	collections?: CollectionAccessView[];
	/** Kept for callers that pass it; who can read is chosen after connecting. */
	readableNodeIds?: string[];
	collectionError?: boolean;
	boundaryNodeId?: string;
	targetCollection?: string;
	/** An existing collection was picked, or "" to create one. */
	onSelect: (nodeId: string, label: string) => void;
	/** The new collection's name input: a form registration or plain props. */
	inputProps: ComponentProps<"input">;
	error?: string;
}

/**
 * Where a new source's content lands: one of the organization's collections,
 * or a new one named here. Every connect form shares it, so who can read the
 * result is said the same way whichever provider is being connected.
 */
export function CollectionField({
	idFor,
	collections,
	collectionError,
	boundaryNodeId,
	onSelect,
	inputProps,
	error,
}: CollectionFieldProps) {
	const selected = collections?.find(
		(collection) => collection.nodeId === boundaryNodeId,
	);
	const listed = !!collections?.length;
	const readers = selected?.grants.map((grant) => grant.subjectLabel) ?? [];
	return (
		<div className="space-y-2">
			{listed && (
				<>
					<Label htmlFor={idFor("existing")}>Collection</Label>
					<Select
						value={boundaryNodeId ?? ""}
						items={[
							{ value: "", label: "A new collection" },
							...(collections ?? []).map((collection) => ({
								value: collection.nodeId,
								label: collection.label,
							})),
						]}
						onValueChange={(value) =>
							onSelect(
								value ?? "",
								collections?.find((collection) => collection.nodeId === value)
									?.label ?? "",
							)
						}
					>
						<SelectTrigger id={idFor("existing")} className="w-full">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="">A new collection</SelectItem>
							{collections?.map((collection) => (
								<SelectItem key={collection.nodeId} value={collection.nodeId}>
									{collection.label}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</>
			)}
			{!selected && (
				<>
					<Label htmlFor={idFor("collection")}>
						{listed ? "New collection name" : "Collection"}
					</Label>
					<Input
						id={idFor("collection")}
						aria-invalid={!!error}
						aria-describedby={error ? idFor("collection-error") : undefined}
						placeholder="e.g. Engineering handbook"
						maxLength={255}
						{...inputProps}
					/>
				</>
			)}
			<p className="type-caption-plain text-muted-foreground">
				{selected
					? readers.length
						? `Already readable by ${readers.join(", ")}.`
						: "Nobody can read this collection yet."
					: "Where its content lands. Connecting doesn't share anything; choose who can read it after."}
			</p>
			{collectionError && (
				<p role="alert" className={errorClass}>
					Couldn’t load this organization’s collections. You can still name a
					new one.
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
