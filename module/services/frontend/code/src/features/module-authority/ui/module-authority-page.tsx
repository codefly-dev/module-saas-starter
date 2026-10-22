"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { isMFARequired, stepUpToast } from "@/features/auth/model/step-up";
import { ConnectError } from "@connectrpc/connect";
import { useState } from "react";
import { toast } from "sonner";
import type { ModuleAuthorityRequest } from "@/gen/saas/accounts/v1/platform_admin_pb";
import { readOutcome, readOutcomeMessage } from "@/shared/lib/read-outcome";
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
	Label,
	Spinner,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	Textarea,
} from "@/shared/ui";
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
export function ModuleAuthorityPage() {
	const read = useModuleEnvelope();
	const write = usePutModuleEnvelope();
	const [draft, setDraft] = useState<Draft | null>(null);
	const [confirming, setConfirming] = useState(false);
	const [refusal, setRefusal] = useState<string | null>(null);

	if (read.isPending) return <Spinner label="Loading the module envelope" />;
	if (read.isError) {
		return (
			<p role="alert" className="text-sm text-muted-foreground">
				{readOutcomeMessage(
					readOutcome(true, read.error),
					"the module envelope",
					"",
				)}{" "}
				{ConnectError.from(read.error).rawMessage}
			</p>
		);
	}

	const envelope = read.data.envelope;
	const current = envelope?.revision ?? BigInt(0);
	const requested = read.data.requested;
	const denied = requested.filter((request) => !request.allowed);
	const base = draft?.base ?? current;
	const text = draft?.text ?? nextRevisionDraft(envelope?.document, current);

	const submit = () =>
		write.mutate(
			{ expectedRevision: base, document: text },
			{
				onSuccess: (response) => {
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
				onSettled: () => setConfirming(false),
			},
		);

	return (
		<div className="space-y-6">
			<div>
				<h1 data-slot="page-title" className="type-page-title">
					Module authority
				</h1>
				<p className="text-muted-foreground">
					The ceiling every module&apos;s authority is derived inside. A module
					holds what its applied manifest asks for and this envelope allows —
					never more than either.
				</p>
			</div>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">Current envelope</CardTitle>
				</CardHeader>
				<CardContent>
					{envelope ? (
						<dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-sm">
							<dt className="text-muted-foreground">Revision</dt>
							<dd className="font-mono">{envelope.revision.toString()}</dd>
							<dt className="text-muted-foreground">Written by</dt>
							<dd className="font-mono">{envelope.writtenBy}</dd>
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
						<Banner tone="danger" title="No module envelope has been written">
							Every module on this host is denied a Work Context until a
							platform super administrator writes one.
						</Banner>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">
						What the applied manifests ask for
					</CardTitle>
					<CardDescription>
						{denied.length === 0
							? "The envelope declares a ceiling for every principal asked for."
							: `${denied.length} of ${requested.length} asked for ${denied.length === 1 ? "has" : "have"} no ceiling in the envelope, and ${denied.length === 1 ? "is" : "are"} denied every Work Context.`}{" "}
						A declared ceiling is necessary, not sufficient: the reconcile pass
						still omits a principal two bindings ask for, or whose manifest no
						longer verifies, and names each in the accounts log.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{requested.length === 0 ? (
						<p className="text-sm text-muted-foreground">
							No applied module manifest asks for authority.
						</p>
					) : (
						<RequestsTable requested={requested} />
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">
						Write revision {(base + ONE).toString()}
					</CardTitle>
					<CardDescription>
						The whole ceiling, as JSON: <code>revision</code> and{" "}
						<code>principals</code>, each principal holding only its{" "}
						<code>grant</code>. It replaces revision {base.toString()} and takes
						effect at the next check, with no restart. Writing it needs a second
						factor completed recently.
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
					{refusal && (
						<Banner
							tone="danger"
							title="Not written"
							onDismiss={() => setRefusal(null)}
						>
							{refusal}
						</Banner>
					)}
					<Label htmlFor="module-envelope-draft">Envelope document</Label>
					<Textarea
						id="module-envelope-draft"
						className="min-h-72 font-mono text-xs"
						spellCheck={false}
						value={text}
						onChange={(event) => setDraft({ base, text: event.target.value })}
					/>
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
							disabled={draft === null}
						>
							Discard draft
						</Button>
					</div>
				</CardContent>
			</Card>

			<AlertDialog open={confirming} onOpenChange={setConfirming}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							Write revision {(base + ONE).toString()}?
						</AlertDialogTitle>
						<AlertDialogDescription>
							It replaces the authority ceiling of every module on this host. A
							principal it no longer allows loses its authority at the next
							check, and an installation it no longer admits is removed.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction disabled={write.isPending} onClick={submit}>
							{write.isPending ? "Writing…" : "Write"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
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
	return (
		<Table aria-label="Authority the applied manifests ask for">
			<TableHeader>
				<TableRow>
					<TableHead>Principal</TableHead>
					<TableHead>Binding</TableHead>
					<TableHead>Envelope</TableHead>
					<TableHead>Asks for</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{requested.map((request) => (
					<TableRow key={`${request.principal}:${request.binding}`}>
						<TableCell className="font-medium">{request.principal}</TableCell>
						<TableCell className="font-mono text-xs">
							{request.binding} · generation {request.generation.toString()}
						</TableCell>
						<TableCell>
							{request.allowed ? (
								<Badge tone="success" dot>
									Ceiling declared
								</Badge>
							) : (
								<Badge tone="danger" dot>
									No ceiling
								</Badge>
							)}
							{(askers.get(request.principal) ?? 0) > 1 && (
								<Badge tone="danger" dot>
									Asked for by {askers.get(request.principal)} bindings
								</Badge>
							)}
						</TableCell>
						<TableCell>
							<details>
								<summary className="cursor-pointer text-sm">
									Authority request
								</summary>
								<pre className="mt-2 max-w-xl overflow-x-auto text-xs">
									{indented(request.authority)}
								</pre>
							</details>
						</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}
