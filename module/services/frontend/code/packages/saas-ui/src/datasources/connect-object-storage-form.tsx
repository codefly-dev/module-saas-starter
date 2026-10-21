"use client";

import { Button, Input, Label } from "@codefly-dev/ui/layout";
import { zodResolver } from "@hookform/resolvers/zod";
import { useId } from "react";
import { useForm, useWatch } from "react-hook-form";
import { CollectionField } from "./collection-field.js";
import { fieldErrorClass as errorClass } from "./credential-mode.js";
import {
	type ConnectObjectStorageValues,
	connectObjectStorageSchema,
} from "./schema.js";
import type { CollectionAccessView } from "./types.js";

interface ConnectObjectStorageFormProps {
	collections?: CollectionAccessView[];
	readableNodeIds?: string[];
	collectionError?: boolean;
	onSubmit: (values: ConnectObjectStorageValues) => void;
	onCancel: () => void;
	isPending: boolean;
	errorMessage?: string;
}

const textFields = [
	{
		name: "endpoint",
		label: "Endpoint",
		placeholder: "https://s3.us-east-1.amazonaws.com",
	},
	{ name: "region", label: "Region", placeholder: "us-east-1" },
	{ name: "bucket", label: "Bucket", placeholder: "acme-knowledge" },
	{ name: "prefix", label: "Prefix (optional)", placeholder: "handbook/" },
	{ name: "accessKeyId", label: "Access key ID", placeholder: "" },
] as const;

export function ConnectObjectStorageForm({
	collections,
	readableNodeIds,
	collectionError,
	onSubmit,
	onCancel,
	isPending,
	errorMessage,
}: ConnectObjectStorageFormProps) {
	const fieldId = useId();
	const idFor = (name: string) => `${fieldId}-${name}`;
	const form = useForm<ConnectObjectStorageValues>({
		resolver: zodResolver(connectObjectStorageSchema),
		defaultValues: {
			endpoint: "",
			region: "",
			bucket: "",
			prefix: "",
			accessKeyId: "",
			secretAccessKey: "",
			boundaryNodeId: "",
			targetCollection: "",
		},
	});
	const { errors } = form.formState;
	const boundaryNodeId = useWatch({
		control: form.control,
		name: "boundaryNodeId",
	});
	const targetCollection = useWatch({
		control: form.control,
		name: "targetCollection",
	});

	return (
		<div
			className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
			role="dialog"
			aria-modal="true"
			aria-label="Connect object storage"
		>
			<div className="w-full max-w-[500px] max-h-[90dvh] overflow-y-auto rounded-lg border bg-card p-6 text-card-foreground shadow-lg">
				<div className="mb-4 space-y-1">
					<h3 className="type-dialog-title">
						Connect object storage
					</h3>
					<p className="type-dialog-description text-muted-foreground">
						Register a bucket as a data source. The objects under its prefix are
						pulled on sync, and only what changed is sent again. Everyone who
						may read the collection reads every object: a bucket has no
						per-object access list, so use one prefix per audience.
					</p>
				</div>

				<form
					onSubmit={form.handleSubmit(onSubmit)}
					className="space-y-4"
					noValidate
				>
					{textFields.map((field) => {
						const error = errors[field.name]?.message;
						return (
							<div key={field.name} className="space-y-2">
								<Label htmlFor={idFor(field.name)}>{field.label}</Label>
								<Input
									id={idFor(field.name)}
									aria-invalid={!!error}
									aria-describedby={
										error ? idFor(`${field.name}-error`) : undefined
									}
									placeholder={field.placeholder}
									{...form.register(field.name)}
								/>
								{error && (
									<p id={idFor(`${field.name}-error`)} className={errorClass}>
										{error}
									</p>
								)}
							</div>
						);
					})}

					<div className="space-y-2">
						<Label htmlFor={idFor("secret")}>Secret access key</Label>
						<Input
							id={idFor("secret")}
							type="password"
							autoComplete="off"
							aria-invalid={!!errors.secretAccessKey}
							aria-describedby={
								errors.secretAccessKey ? idFor("secret-error") : undefined
							}
							{...form.register("secretAccessKey")}
						/>
						<p className="type-caption-plain text-muted-foreground">
							Encrypted when the host receives it and never shown again. Give
							the key read and list access to this bucket only.
						</p>
						{errors.secretAccessKey && (
							<p id={idFor("secret-error")} className={errorClass}>
								{errors.secretAccessKey.message}
							</p>
						)}
					</div>

					<CollectionField
						idFor={idFor}
						{...(collections ? { collections } : {})}
						{...(readableNodeIds ? { readableNodeIds } : {})}
						{...(collectionError ? { collectionError } : {})}
						{...(boundaryNodeId ? { boundaryNodeId } : {})}
						targetCollection={targetCollection}
						onSelect={(nodeId, label) => {
							form.setValue("boundaryNodeId", nodeId);
							form.setValue("targetCollection", label);
						}}
						inputProps={form.register("targetCollection")}
						{...(errors.targetCollection?.message
							? { error: errors.targetCollection.message }
							: {})}
					/>

					{errorMessage && (
						<p role="alert" className={errorClass}>
							{errorMessage}
						</p>
					)}

					<div className="flex justify-end gap-2 pt-2">
						<Button type="button" onClick={onCancel} variant="outline">
							Cancel
						</Button>
						<Button type="submit" disabled={isPending} aria-busy={isPending}>
							{isPending ? "Connecting…" : "Connect bucket"}
						</Button>
					</div>
				</form>
			</div>
		</div>
	);
}
