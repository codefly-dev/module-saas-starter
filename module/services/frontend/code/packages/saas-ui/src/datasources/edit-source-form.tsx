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
						Change what it brings in. The repository
						{source.boundaryLabel
							? `, the ${source.boundaryLabel} collection`
							: ""}{" "}
						and how it connects stay as they are.
					</p>
					{/*
					 * Said separately, and said at all, because narrowing a filter is
					 * the edit whose consequence is least obvious. Saving deletes
					 * nothing: the host hands off the files in the new scope and emits
					 * no removal for the ones that left it, so whether a consuming
					 * module drops what the new snapshot no longer names is that
					 * module's rule, not something this host decides or can report.
					 * Promising that de-scoped content is retained would be a guess,
					 * and promising it is removed would be a guess in the direction
					 * someone clearing out files they should not have ingested would
					 * act on.
					 */}
					<p className="type-caption-plain text-muted-foreground">
						Saving deletes nothing already brought in; the next sync brings in
						what you chose.
					</p>
				</div>

				<form
					onSubmit={form.handleSubmit(onSubmit)}
					className="space-y-4"
					noValidate
				>
					<div className="space-y-2">
						<Label htmlFor={idFor("branch")}>Branch</Label>
						<Input
							id={idFor("branch")}
							aria-invalid={!!errors.branch}
							aria-describedby={
								errors.branch ? idFor("branch-error") : undefined
							}
							placeholder="main"
							{...form.register("branch")}
						/>
						<p className="type-caption-plain text-muted-foreground">
							Leave empty for the repository&apos;s default branch.
						</p>
						{errors.branch && (
							<p id={idFor("branch-error")} className={errorClass}>
								{errors.branch.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor={idFor("paths")}>Folders</Label>
						<Textarea
							id={idFor("paths")}
							className="resize-none font-mono"
							rows={2}
							placeholder={"docs/\nhandbook/"}
							{...form.register("paths")}
						/>
						<p className="type-caption-plain text-muted-foreground">
							One per line. Leave empty for the whole repository.
						</p>
						{errors.paths && (
							<p role="alert" className={errorClass}>
								{errors.paths.message}
							</p>
						)}
					</div>

					<div className="space-y-2">
						<Label htmlFor={idFor("extensions")}>File types</Label>
						<Input
							id={idFor("extensions")}
							placeholder=".md, .mdx"
							aria-invalid={!!errors.fileExtensions}
							{...form.register("fileExtensions")}
						/>
						<p className="type-caption-plain text-muted-foreground">
							Separate with commas, e.g. .md, .mdx. Leave empty for every type.
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
							{isPending ? "Checking access…" : "Save changes"}
						</Button>
					</div>
				</form>
			</div>
		</div>
	);
}
