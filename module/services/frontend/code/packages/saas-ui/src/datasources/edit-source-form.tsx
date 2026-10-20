"use client";

import { Button, Input, Label, Textarea } from "@codefly-dev/ui/layout";
import { zodResolver } from "@hookform/resolvers/zod";
import { useId } from "react";
import { useForm } from "react-hook-form";
import { fieldErrorClass as errorClass } from "./credential-mode.js";
import { type EditSourceValues, editSourceSchema } from "./schema.js";
import type { DatasourceView } from "./types.js";

interface EditSourceFormProps {
	/** The source as the host last reported it; its values seed the fields. */
	source: DatasourceView;
	onSubmit: (values: EditSourceValues) => void;
	onCancel: () => void;
	isPending: boolean;
	errorMessage?: string;
}

/**
 * Changes what an already-connected source fetches: its branch and its path and
 * file-type filters, bound by the same schema the connect form uses, so an edit
 * can never store a scope connecting would have refused.
 *
 * It offers exactly the connect form's *mutable* fields, and the repository and
 * the collection are shown as the standing facts they are rather than as
 * disabled inputs. Both are the identity of something a consumer already holds
 * — a source pointed at another repository is a different source, and one
 * writing into another collection replaces that collection's contents — so the
 * host refuses to move either, and offering them greyed out would read as "not
 * yet" rather than "not here".
 *
 * The fields are seeded from the source and submitted whole, because that is
 * what the RPC means: it replaces the three values rather than patching them,
 * so a field left as it arrived is what keeps it, and a field cleared clears
 * it.
 */
export function EditSourceForm({
	source,
	onSubmit,
	onCancel,
	isPending,
	errorMessage,
}: EditSourceFormProps) {
	const fieldId = useId();
	const idFor = (name: string) => `${fieldId}-${name}`;
	const form = useForm<EditSourceValues>({
		resolver: zodResolver(editSourceSchema),
		defaultValues: {
			branch: source.branch,
			// One prefix per line, as the connect form's field is parsed, so the
			// value a person sees is the value they would have typed.
			paths: source.paths.join("\n"),
			fileExtensions: (source.fileExtensions ?? []).join(", "),
		},
	});
	const { errors } = form.formState;

	return (
		<div
			className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
			role="dialog"
			aria-modal="true"
			aria-label={`Edit ${source.repo}`}
		>
			<div className="w-full max-w-[500px] max-h-[90dvh] overflow-y-auto rounded-lg border bg-card p-6 text-card-foreground shadow-lg">
				<div className="mb-4 space-y-1">
					<h3 className="type-dialog-title">Edit {source.repo}</h3>
					<p className="type-dialog-description text-muted-foreground">
						Change what this source fetches. Its repository
						{source.boundaryLabel
							? `, its collection ${source.boundaryLabel}`
							: ""}{" "}
						and its credential stay as they are, and so do its sync history and
						everything already ingested.
					</p>
				</div>

				<form
					onSubmit={form.handleSubmit(onSubmit)}
					className="space-y-4"
					noValidate
				>
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
						<p className="type-caption-plain text-muted-foreground">
							Changing the branch re-snapshots the source: the consuming module
							receives the new branch&apos;s files as one change set.
						</p>
						{errors.branch && (
							<p id={idFor("branch-error")} className={errorClass}>
								{errors.branch.message}
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
						<p className="type-caption-plain text-muted-foreground">
							One path prefix per line. Leave empty to ingest the whole repo.
						</p>
						{errors.paths && (
							<p role="alert" className={errorClass}>
								{errors.paths.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor={idFor("extensions")}>File types (optional)</Label>
						<Input
							id={idFor("extensions")}
							placeholder=".md, .mdx"
							aria-invalid={!!errors.fileExtensions}
							{...form.register("fileExtensions")}
						/>
						<p className="type-caption-plain text-muted-foreground">
							Comma-separated suffixes, case-insensitive. Empty includes all
							file types.
						</p>
						{errors.fileExtensions && (
							<p role="alert" className={errorClass}>
								{errors.fileExtensions.message}
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
							{isPending ? "Validating GitHub access…" : "Save changes"}
						</Button>
					</div>
				</form>
			</div>
		</div>
	);
}
