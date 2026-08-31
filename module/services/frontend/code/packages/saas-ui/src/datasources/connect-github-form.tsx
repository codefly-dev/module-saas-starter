"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useId } from "react";
import { useForm } from "react-hook-form";
import { type ConnectGitHubValues, connectGitHubSchema } from "./schema.js";
import { cn } from "./util.js";

interface ConnectGitHubFormProps {
	onSubmit: (values: ConnectGitHubValues) => void;
	onCancel: () => void;
	isPending: boolean;
	errorMessage?: string;
}

const fieldClass =
	"flex w-full rounded-md border border-input bg-transparent px-3 py-1.5 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

const labelClass = "text-sm font-medium";

const errorClass = "text-sm text-destructive";

export function ConnectGitHubForm({
	onSubmit,
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
			paths: "",
			branch: "",
			targetCollection: "",
			accessToken: "",
			webhookSecret: "",
		},
	});
	const { errors } = form.formState;

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
						<label className={labelClass} htmlFor={idFor("repo")}>
							Repository
						</label>
						<input
							id={idFor("repo")}
							className={fieldClass}
							placeholder="owner/name"
							{...form.register("repo")}
						/>
						{errors.repo && <p className={errorClass}>{errors.repo.message}</p>}
					</div>

					<div className="space-y-2">
						<label className={labelClass} htmlFor={idFor("paths")}>
							Paths (optional)
						</label>
						<textarea
							id={idFor("paths")}
							className={cn(fieldClass, "resize-none font-mono")}
							rows={2}
							placeholder={"docs/\nsrc/api/"}
							{...form.register("paths")}
						/>
						<p className="text-xs text-muted-foreground">
							One path prefix per line. Leave empty to ingest the whole repo.
						</p>
					</div>

					<div className="space-y-2">
						<label className={labelClass} htmlFor={idFor("branch")}>
							Branch (optional)
						</label>
						<input
							id={idFor("branch")}
							className={fieldClass}
							placeholder="Defaults to the repository default branch"
							{...form.register("branch")}
						/>
						{errors.branch && (
							<p className={errorClass}>{errors.branch.message}</p>
						)}
					</div>

					<div className="space-y-2">
						<label className={labelClass} htmlFor={idFor("collection")}>
							Target collection
						</label>
						<input
							id={idFor("collection")}
							className={fieldClass}
							placeholder="Documents-store collection to land entries in"
							{...form.register("targetCollection")}
						/>
						{errors.targetCollection && (
							<p className={errorClass}>{errors.targetCollection.message}</p>
						)}
					</div>

					<div className="space-y-2">
						<label className={labelClass} htmlFor={idFor("token")}>
							Access token
						</label>
						<input
							id={idFor("token")}
							type="password"
							className={fieldClass}
							placeholder="PAT or GitHub App installation token"
							{...form.register("accessToken")}
						/>
						{errors.accessToken && (
							<p className={errorClass}>{errors.accessToken.message}</p>
						)}
					</div>

					<div className="space-y-2">
						<label className={labelClass} htmlFor={idFor("secret")}>
							Webhook secret (optional)
						</label>
						<input
							id={idFor("secret")}
							type="password"
							className={fieldClass}
							placeholder="Shared secret GitHub signs push deliveries with"
							{...form.register("webhookSecret")}
						/>
						<p className="text-xs text-muted-foreground">
							Enables live webhook ingestion. Add it later if you don&apos;t
							have it yet.
						</p>
						{errors.webhookSecret && (
							<p className={errorClass}>{errors.webhookSecret.message}</p>
						)}
					</div>

					{errorMessage && (
						<p role="alert" className={errorClass}>
							{errorMessage}
						</p>
					)}

					<div className="flex justify-end gap-2 pt-2">
						<button
							type="button"
							onClick={onCancel}
							className="inline-flex h-9 items-center rounded-md border px-4 text-sm font-medium shadow-sm hover:bg-accent"
						>
							Cancel
						</button>
						<button
							type="submit"
							disabled={isPending}
							className="inline-flex h-9 items-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow-sm hover:bg-primary/90 disabled:opacity-50"
						>
							{isPending ? "Connecting…" : "Connect"}
						</button>
					</div>
				</form>
			</div>
		</div>
	);
}
