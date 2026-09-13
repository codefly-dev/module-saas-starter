"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useId } from "react";
import { useForm, useWatch } from "react-hook-form";
import { type ConnectGitHubValues, connectGitHubSchema } from "./schema.js";
import type { CollectionAccessView } from "./types.js";
import { Button, Input, Label, Textarea } from "@codefly-dev/ui/layout";

interface ConnectGitHubFormProps {
	collections?: CollectionAccessView[];
	readableNodeIds?: string[];
	collectionError?: boolean;
	onSubmit: (values: ConnectGitHubValues) => void;
	onCancel: () => void;
	isPending: boolean;
	errorMessage?: string;
}

const errorClass = "text-sm text-destructive";

export function ConnectGitHubForm({
	onSubmit,
	collections,
	readableNodeIds,
	collectionError,
	onCancel,
	isPending,
	errorMessage,
}: ConnectGitHubFormProps) {
	// Unique per instance so two mounted forms don't collide on id/htmlFor
	// (breaks label association + a11y when the panel is reused in more than one
	// place).
	const fieldId = useId();
	const idFor = (name: string) => `${fieldId}-${name}`;
	const form = useForm<ConnectGitHubValues>({
		resolver: zodResolver(connectGitHubSchema),
		defaultValues: {
			repo: "",
			boundaryNodeId: "",
			paths: "",
			branch: "",
			targetCollection: "",
			accessToken: "",
			webhookSecret: "",
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
	const selected = collections?.find(
		(collection) => collection.nodeId === boundaryNodeId,
	);
	const named =
		selected ??
		collections?.find((collection) => collection.label === targetCollection);

	return (
		<div
			className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
			role="dialog"
			aria-modal="true"
			aria-label="Connect GitHub"
		>
			<div className="w-full max-w-[500px] rounded-lg border bg-card p-6 text-card-foreground shadow-lg">
				<div className="mb-4 space-y-1">
					<h3 className="text-lg font-semibold tracking-tight">
						Connect GitHub
					</h3>
					<p className="text-sm text-muted-foreground">
						Register a repository as a data source. Its contents are pulled and
						enqueued for ingestion.
					</p>
				</div>

				<form
					onSubmit={form.handleSubmit(onSubmit)}
					className="space-y-4"
					noValidate
				>
					<div className="space-y-2">
						<Label htmlFor={idFor("repo")}>Repository</Label>
						<Input
							id={idFor("repo")}
							aria-invalid={!!errors.repo}
							aria-describedby={errors.repo ? idFor("repo-error") : undefined}
							placeholder="owner/name"
							{...form.register("repo")}
						/>
						{errors.repo && (
							<p id={idFor("repo-error")} className={errorClass}>
								{errors.repo.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor={idFor("paths")}>Paths (optional)</Label>
						<Textarea
							id={idFor("paths")}
							className="resize-none font-mono"
							rows={2}
							placeholder={"docs/\nsrc/api/"}
							{...form.register("paths")}
						/>
						<p className="text-xs text-muted-foreground">
							One path prefix per line. Leave empty to ingest the whole repo.
						</p>
					</div>

					<div className="space-y-2">
						<Label htmlFor={idFor("branch")}>Branch (optional)</Label>
						<Input
							id={idFor("branch")}
							aria-invalid={!!errors.branch}
							aria-describedby={
								errors.branch ? idFor("branch-error") : undefined
							}
							placeholder="Defaults to the repository default branch"
							{...form.register("branch")}
						/>
						{errors.branch && (
							<p id={idFor("branch-error")} className={errorClass}>
								{errors.branch.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						{collections && (
							<>
								<Label htmlFor={idFor("existing")}>Existing collection</Label>
								<select
									id={idFor("existing")}
									aria-label="Existing collection"
									value={boundaryNodeId ?? ""}
									onChange={(event) => {
										form.setValue("boundaryNodeId", event.target.value);
										form.setValue(
											"targetCollection",
											collections.find(
												(collection) => collection.nodeId === event.target.value,
											)?.label ?? "",
										);
									}}
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
							aria-invalid={!!errors.targetCollection}
							aria-describedby={
								errors.targetCollection ? idFor("collection-error") : undefined
							}
							placeholder="Documents-store collection to land entries in"
							{...form.register("targetCollection")}
						/>
						<p className="text-sm">
							Connecting grants no read access to the creator or a default team.
							Organization administrators explicitly grant members or teams read
							access.
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
						{errors.targetCollection && (
							<p id={idFor("collection-error")} className={errorClass}>
								{errors.targetCollection.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor={idFor("token")}>Access token</Label>
						<Input
							id={idFor("token")}
							aria-invalid={!!errors.accessToken}
							aria-describedby={
								errors.accessToken ? idFor("token-error") : undefined
							}
							type="password"
							placeholder="PAT or GitHub App installation token"
							{...form.register("accessToken")}
						/>
						<p className="text-xs text-muted-foreground">
							Use a fine-grained PAT restricted to this repository with
							Contents: Read-only. Your organization may require approval or SSO
							authorization. Repository and branch access are verified before
							saving.
						</p>
						{errors.accessToken && (
							<p id={idFor("token-error")} className={errorClass}>
								{errors.accessToken.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor={idFor("secret")}>Webhook secret (optional)</Label>
						<Input
							id={idFor("secret")}
							aria-invalid={!!errors.webhookSecret}
							aria-describedby={
								errors.webhookSecret ? idFor("secret-error") : undefined
							}
							type="password"
							placeholder="Shared secret GitHub signs push deliveries with"
							{...form.register("webhookSecret")}
						/>
						<p className="text-xs text-muted-foreground">
							Enables live webhook ingestion. Add it later if you don&apos;t
							have it yet.
						</p>
						{errors.webhookSecret && (
							<p id={idFor("secret-error")} className={errorClass}>
								{errors.webhookSecret.message}
							</p>
						)}
					</div>

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
							{isPending ? "Validating GitHub access…" : "Validate and connect"}
						</Button>
					</div>
				</form>
			</div>
		</div>
	);
}
