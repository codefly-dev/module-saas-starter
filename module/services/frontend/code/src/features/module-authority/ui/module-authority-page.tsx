"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { useRef, useState } from "react";
import { toast } from "sonner";
import { UserLabel } from "@/components/user-label";
import { isMFARequired, stepUpToast } from "@/features/auth/model/step-up";
import type { ModuleAuthorityRequest } from "@/gen/saas/accounts/v1/platform_admin_pb";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Badge,
	Banner,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	ErrorState,
	Field,
	Page as PageBody,
	PageHeader,
	Panel,
	Spinner,
	Textarea,
	useLoadingPhase,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { nextRevisionDraft, writeRefusal } from "../model/draft";
import { usePutModuleEnvelope } from "../service/mutations";
import { useModuleEnvelope } from "../service/queries";

const ONE = BigInt(1);

/** A draft remembers the revision it was started from: the write's fence. */
type Draft = { base: bigint; text: string };

/**
 * Module authority: the envelope a platform super administrator writes — the
 * ceiling every module's authority is derived inside — beside what the applied
 * module manifests ask for, so the principals it does not allow are named
 * here instead of surfacing only as denied mints.
 */
const header = (
	<PageHeader
		title="Module access"
		description="Review the access requested by installed modules and manage the limits they must stay within."
	/>
);

export function ModuleAuthorityPage() {
	const read = useModuleEnvelope();
	const write = usePutModuleEnvelope();
	const pending = useRef(false);
	const [draft, setDraft] = useState<Draft | null>(null);
	const [confirming, setConfirming] = useState(false);
	const [refusal, setRefusal] = useState<string | null>(null);

	const { indicator } = useLoadingPhase(read.isPending);

	if (read.isPending) {
		return (
			<PageBody>
				{header}
				{indicator && <Spinner label="Loading module access" />}
			</PageBody>
		);
	}
	if (read.isError) {
		return (
			<PageBody>
				{header}
				<Panel>
					<ErrorState
						title="Module access can’t be shown"
						detail={
							readOutcome(true, read.error) === "forbidden"
								? "You don’t have access to this configuration. Ask a platform administrator for access."
								: failureText("load module access", read.error)
						}
					/>
				</Panel>
			</PageBody>
		);
	}

	const envelope = read.data.envelope;
	const current = envelope?.revision ?? BigInt(0);
	const requested = read.data.requested;
	const denied = requested.filter((request) => !request.allowed);
	const base = draft?.base ?? current;
	const text = draft?.text ?? nextRevisionDraft(envelope?.document, current);

	const submit = () => {
		if (pending.current) return;
		pending.current = true;
		write.mutate(
			{ expectedRevision: base, document: text },
			{
				onSuccess: (response) => {
					setConfirming(false);
					toast.success(
						`Revision ${response.envelope?.revision ?? base + ONE} written`,
					);
					setDraft(null);
					setRefusal(null);
				},
				onError: (error) => {
					if (isMFARequired(error)) stepUpToast();
					setRefusal(writeRefusal(error));
				},
				onSettled: () => {
					pending.current = false;
				},
			},
		);
	};

	return (
		<PageBody>
			{header}

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2} className="text-base">
						Current configuration
					</CardTitle>
				</CardHeader>
				<CardContent>
					{envelope ? (
						<dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm">
							<dt className="text-muted-foreground">Revision</dt>
							<dd className="font-mono">{envelope.revision.toString()}</dd>
							<dt className="text-muted-foreground">Written by</dt>
							<dd>
								<UserLabel userId={envelope.writtenBy} />
							</dd>
							<dt className="text-muted-foreground">Written at</dt>
							<dd>
								{formatDate(
									envelope.writtenAt
										? timestampDate(envelope.writtenAt).toISOString()
										: undefined,
								)}
							</dd>
						</dl>
					) : (
						<Banner tone="danger" title="Module access hasn’t been configured">
							Modules cannot run until a platform administrator saves their
							access configuration.
						</Banner>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2} className="text-base">
						Requested access
					</CardTitle>
					<CardDescription>
						{requested.length === 0
							? "Installed modules will appear here when they request access."
							: denied.length === 0
								? "Every request is within the configured access limits."
								: `${denied.length} of ${requested.length} requests are outside the configured access limits.`}{" "}
						Duplicate or unverified requests cannot be activated.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{requested.length === 0 ? (
						<p className="text-sm text-muted-foreground">
							No modules have requested access.
						</p>
					) : (
						<RequestsTable requested={requested} />
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2} className="text-base">
						Write revision {(base + ONE).toString()}
					</CardTitle>
					<CardDescription>
						Review the complete configuration before saving. This replaces
						revision {base.toString()} and applies without a restart. Confirm
						your passkey before saving.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3">
					{base !== current && (
						<Banner tone="warning">
							Revision {current.toString()} was written after this draft was
							started from revision {base.toString()}, so writing it will be
							refused. Discard the draft to start from the current revision.
						</Banner>
					)}
					{refusal && !confirming && (
						<Banner
							tone="danger"
							title="Not written"
							onDismiss={() => setRefusal(null)}
						>
							{refusal}
						</Banner>
					)}
					<Field label="Configuration document">
						{(control) => (
							<Textarea
								{...control}
								className="min-h-72 font-mono text-xs"
								spellCheck={false}
								value={text}
								disabled={write.isPending}
								onChange={(event) =>
									setDraft({ base, text: event.target.value })
								}
							/>
						)}
					</Field>
					<div className="flex gap-2">
						<Button
							onClick={() => setConfirming(true)}
							disabled={write.isPending}
						>
							Write revision {(base + ONE).toString()}
						</Button>
						<Button
							variant="outline"
							onClick={() => {
								setDraft(null);
								setRefusal(null);
							}}
							disabled={draft === null || write.isPending}
						>
							Discard draft
						</Button>
					</div>
				</CardContent>
			</Card>

			<AlertDialog
				open={confirming}
				onOpenChange={(open) => !pending.current && setConfirming(open)}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							Write revision {(base + ONE).toString()}?
						</AlertDialogTitle>
						<AlertDialogDescription>
							This replaces the access limits for every installed module.
							Modules that no longer qualify lose access, and installations
							excluded by the new configuration are removed.
						</AlertDialogDescription>
					</AlertDialogHeader>
					{refusal && <ErrorState title="Not written" detail={refusal} />}
					<AlertDialogFooter>
						<AlertDialogCancel disabled={write.isPending}>
							Cancel
						</AlertDialogCancel>
						<AlertDialogAction
							disabled={write.isPending}
							onClick={(event) => {
								event.preventDefault();
								submit();
							}}
						>
							{write.isPending ? "Writing…" : "Write"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</PageBody>
	);
}

/** The authority request, indented; shown as sent when it is not JSON. */
function indented(authority: string): string {
	try {
		return JSON.stringify(JSON.parse(authority), null, 2);
	} catch {
		return authority;
	}
}

function RequestsTable({ requested }: { requested: ModuleAuthorityRequest[] }) {
	// Two bindings asking for one principal leave it with no single answer, so
	// the pass omits it whatever the envelope says.
	const askers = new Map<string, number>();
	for (const request of requested) {
		askers.set(request.principal, (askers.get(request.principal) ?? 0) + 1);
	}
	const col = createColumnHelper<ModuleAuthorityRequest>();
	const table = useReactTable({
		data: requested,
		getCoreRowModel: getCoreRowModel(),
		columns: [
			col.accessor("principal", {
				header: "Module",
				cell: ({ row }) => (
					<span className="font-medium">
						{row.original.alias || row.original.principal}
					</span>
				),
			}),
			col.accessor("generation", {
				header: "Version",
				cell: ({ getValue }) => getValue().toString(),
			}),
			col.accessor("allowed", {
				header: "Access",
				cell: ({ row }) => (
					<div className="flex flex-wrap gap-2">
						<Badge tone={row.original.allowed ? "success" : "danger"} dot>
							{row.original.allowed ? "Within limits" : "Not configured"}
						</Badge>
						{(askers.get(row.original.principal) ?? 0) > 1 && (
							<Badge tone="danger" dot>
								{askers.get(row.original.principal)} conflicting requests
							</Badge>
						)}
					</div>
				),
			}),
			col.accessor("authority", {
				header: "Configuration",
				cell: ({ getValue }) => (
					<details>
						<summary className="cursor-pointer rounded-sm text-sm focus-visible:outline-2 focus-visible:outline-ring">
							Configuration details
						</summary>
						<pre className="mt-2 max-w-xl overflow-x-auto text-xs">
							{indented(getValue())}
						</pre>
					</details>
				),
			}),
		],
	});
	return <DataTable table={table} ariaLabel="Requested module access" />;
}
