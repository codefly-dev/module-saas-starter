"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { Button, Checkbox, Input, Label } from "@/shared/ui";
import {
	type AuditExportFormValues,
	auditExportFormSchema,
} from "../model/schemas";

interface Props {
	// Initial values — the AuditExportConfig from the api on edit, or
	// undefined on first-config. The api Get returns "" for
	// secretAccessKey so the form never displays a stored secret; the
	// input is left blank and submitting "" preserves the existing.
	initial?: AuditExportFormValues;
	isPending: boolean;
	onSubmit: (values: AuditExportFormValues) => void;
}

const defaults: AuditExportFormValues = {
	bucket: "",
	region: "us-east-1",
	endpoint: "",
	prefix: "",
	accessKeyId: "",
	secretAccessKey: "",
	cadenceMinutes: 60,
	enabled: true,
};

/**
 * AuditExportForm — operator-facing form for the per-org audit-log
 * S3 export. The api owns the persisted shape; this is just a
 * validated input gate.
 *
 * Two flows in one form:
 *   - First-config: secretAccessKey is required, bucket / endpoint
 *     fields are blank, "Save" is the CTA.
 *   - Edit: secretAccessKey is left blank (api Get returns "" for it),
 *     submitting "" preserves the stored secret. CTA flips to "Update".
 */
export function AuditExportForm({ initial, isPending, onSubmit }: Props) {
	const form = useForm<AuditExportFormValues>({
		resolver: zodResolver(auditExportFormSchema),
		defaultValues: { ...defaults, ...(initial ?? {}) },
	});

	// When we navigate between orgs the parent re-renders with new
	// `initial`; reset the form so we don't carry the previous org's
	// values forward.
	useEffect(() => {
		form.reset({ ...defaults, ...(initial ?? {}) });
	}, [initial, form]);

	const isEditing = !!initial?.bucket;

	return (
		<form
			onSubmit={form.handleSubmit(onSubmit)}
			className="space-y-5 max-w-2xl"
		>
			<div className="grid grid-cols-2 gap-4">
				<div className="space-y-2 col-span-2">
					<Label htmlFor="bucket">Bucket</Label>
					<Input
						id="bucket"
						placeholder="my-org-audit-logs"
						{...form.register("bucket")}
					/>
					{form.formState.errors.bucket && (
						<p className="text-sm text-destructive">
							{form.formState.errors.bucket.message}
						</p>
					)}
				</div>

				<div className="space-y-2">
					<Label htmlFor="region">Region</Label>
					<Input
						id="region"
						placeholder="us-east-1"
						{...form.register("region")}
					/>
				</div>

				<div className="space-y-2">
					<Label htmlFor="cadence">Cadence (minutes)</Label>
					<Input
						id="cadence"
						type="number"
						min={5}
						max={10080}
						{...form.register("cadenceMinutes", { valueAsNumber: true })}
					/>
					{form.formState.errors.cadenceMinutes && (
						<p className="text-sm text-destructive">
							{form.formState.errors.cadenceMinutes.message}
						</p>
					)}
				</div>

				<div className="space-y-2 col-span-2">
					<Label htmlFor="endpoint">Endpoint (optional)</Label>
					<Input
						id="endpoint"
						placeholder="leave empty for AWS S3 in the chosen region"
						{...form.register("endpoint")}
					/>
					<p className="text-xs text-muted-foreground">
						Override for S3-compatible stores (R2, MinIO, GCS S3-mode). Use{" "}
						<code className="rounded bg-muted px-1 py-0.5 font-mono">
							http://host:port
						</code>{" "}
						to disable TLS (local MinIO);{" "}
						<code className="rounded bg-muted px-1 py-0.5 font-mono">
							host:port
						</code>{" "}
						or{" "}
						<code className="rounded bg-muted px-1 py-0.5 font-mono">
							https://host:port
						</code>{" "}
						keeps it on.
					</p>
				</div>

				<div className="space-y-2 col-span-2">
					<Label htmlFor="prefix">Object key prefix (optional)</Label>
					<Input
						id="prefix"
						placeholder="audit/ — leave empty to drop at bucket root"
						{...form.register("prefix")}
					/>
				</div>

				<div className="space-y-2">
					<Label htmlFor="accessKey">Access key id</Label>
					<Input
						id="accessKey"
						autoComplete="off"
						{...form.register("accessKeyId")}
					/>
					{form.formState.errors.accessKeyId && (
						<p className="text-sm text-destructive">
							{form.formState.errors.accessKeyId.message}
						</p>
					)}
				</div>

				<div className="space-y-2">
					<Label htmlFor="secretKey">
						Secret access key
						{isEditing && (
							<span className="ml-2 text-xs font-normal text-muted-foreground">
								(leave blank to keep existing)
							</span>
						)}
					</Label>
					<Input
						id="secretKey"
						type="password"
						autoComplete="new-password"
						placeholder={isEditing ? "•••••••• (preserved)" : ""}
						{...form.register("secretAccessKey")}
					/>
				</div>
			</div>

			<div className="flex items-start gap-2 rounded-md border p-3">
				<Checkbox
					id="enabled"
					checked={form.watch("enabled")}
					onCheckedChange={(v) => form.setValue("enabled", v === true)}
				/>
				<div className="space-y-0.5">
					<Label htmlFor="enabled" className="cursor-pointer">
						Enable exports
					</Label>
					<p className="text-xs text-muted-foreground">
						When off, exports stop on the next exporter tick. Cadence and
						credentials are preserved so re-enabling resumes from the last
						successful checkpoint.
					</p>
				</div>
			</div>

			<div className="flex justify-end">
				<Button type="submit" disabled={isPending}>
					{isPending
						? isEditing
							? "Updating…"
							: "Saving…"
						: isEditing
							? "Update"
							: "Save"}
				</Button>
			</div>
		</form>
	);
}
