"use client";

import { timestampDate } from "@bufbuild/protobuf/wkt";
import { DescriptionList, Disclosure } from "@codefly-dev/ui/layout";
import { ShieldCheck } from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { UserLabel } from "@/components/user-label";
import { isMFARequired, stepUpToast } from "@/features/auth/model/step-up";
import type { ModuleAuthorityRequest } from "@/gen/saas/accounts/v1/platform_admin_pb";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	Banner,
	Button,
	ConfirmDialog,
	EmptyState,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Section,
	Stack,
} from "@/shared/ui";
import {
	draftChanges,
	draftProblem,
	nextRevisionDraft,
	withoutModule,
	writeRefusal,
} from "../model/draft";
import { saveConsequences, saveQuestion } from "../model/save-question";
import { usePutModuleEnvelope } from "../service/mutations";
import { useModuleEnvelope } from "../service/queries";
import { PermissionsEditor } from "./permissions-editor";
import { RequestDetails, RequestsTable } from "./requests-table";

// The rows when there is no answer yet: one array for every render.
const NO_REQUESTS: ModuleAuthorityRequest[] = [];

/** Edits remember the version they were started from: the save's fence. */
type Draft = { base: bigint; text: string };

/** Where a refused save is reported: beside the act that was refused. */
type Refusal = { from: "revoke" | "save"; text: string };

const header = (
	<PageHeader
		title="Module permissions"
		description="Approve what each installed module is allowed to do on this platform. A module can do only what it asks for and a platform operator has approved, never more than either."
	/>
);

/**
 * Module permissions: what each installed module asks to be allowed to do,
 * whether a platform operator approved it, and the document those approvals
 * are kept in. A module that asks and was never approved is named here rather
 * than surfacing only as work that silently never runs.
 */
export function ModuleAuthorityPage() {
	const read = useModuleEnvelope();
	const write = usePutModuleEnvelope();
	const [draft, setDraft] = useState<Draft | null>(null);
	const [confirmingSave, setConfirmingSave] = useState(false);
	const [revoking, setRevoking] = useState<string | null>(null);
	const [refusal, setRefusal] = useState<Refusal | null>(null);

	const envelope = read.data?.envelope;
	const document = envelope?.document;
	const current = envelope?.revision ?? BigInt(0);
	const canRevoke = useCallback(
		(module: string) => withoutModule(document, current, module) !== null,
		[document, current],
	);

	if (read.isError) {
		const forbidden = readOutcome(true, read.error) === "forbidden";
		return (
			<PageBody>
				{header}
				<Panel>
					{forbidden ? (
						<ErrorState
							title="You don't have access to module permissions"
							detail="Only a platform super administrator can see or change them. Ask one to give you that role."
						/>
					) : (
						<Stack direction="row" gap={4} align="center" justify="between">
							<ErrorState
								title="Module permissions couldn't be loaded"
								detail="Nothing was changed. If it keeps failing, tell the team that runs this platform."
							/>
							<Button variant="outline" onClick={() => void read.refetch()}>
								Retry
							</Button>
						</Stack>
					)}
				</Panel>
			</PageBody>
		);
	}

	const loading = read.isPending;
	const requested = read.data?.requested ?? NO_REQUESTS;
	const waiting = requested.filter((request) => !request.allowed);
	const base = draft?.base ?? current;
	const text = draft?.text ?? nextRevisionDraft(document, current);
	const changes = draftChanges(document, text);

	const save = (
		from: Refusal["from"],
		input: { expectedRevision: bigint; document: string },
		saved: string,
	) =>
		write.mutate(input, {
			onSuccess: () => {
				toast.success(saved);
				if (from === "save") setDraft(null);
				setRefusal(null);
			},
			onError: (error) => {
				if (isMFARequired(error)) stepUpToast();
				setRefusal({ from, text: writeRefusal(error) });
			},
			onSettled: () => {
				setConfirmingSave(false);
				setRevoking(null);
			},
		});

	return (
		<PageBody>
			{header}

			{!loading && !envelope && (
				<Banner tone="danger" title="No module has been approved yet">
					Every installed module is blocked from doing any work for
					organizations until a platform operator approves its permissions
					below.
				</Banner>
			)}

			<Section
				title="What modules are asking for"
				description={
					loading || requested.length === 0
						? undefined
						: `${
								waiting.length === 0
									? "Every module asking for permissions is approved."
									: `${waiting.length} of ${requested.length} ${waiting.length === 1 ? "is" : "are"} waiting for approval and can't do any work for organizations until approved.`
							} An approved module is still blocked if it is installed twice under one name or its installation can no longer be verified.`
				}
			>
				{refusal?.from === "revoke" && (
					<Banner
						tone="danger"
						title="Not saved"
						onDismiss={() => setRefusal(null)}
					>
						{refusal.text}
					</Banner>
				)}
				{!loading && requested.length === 0 ? (
					<EmptyState
						icon={<ShieldCheck />}
						heading="No module is asking for permissions"
						description="When a module that needs permissions is installed on this platform, what it asks for appears here for a platform operator to approve."
					/>
				) : (
					<>
						<RequestsTable
							requested={requested}
							isLoading={loading}
							canRevoke={canRevoke}
							onRevoke={setRevoking}
						/>
						{!loading && <RequestDetails requested={requested} />}
					</>
				)}
			</Section>

			{!loading && (
				<>
					{envelope && (
						<Section title="Permissions in force">
							<DescriptionList
								items={[
									{ term: "Version", value: envelope.revision.toString() },
									{
										term: "Last changed",
										value: formatDate(
											envelope.writtenAt
												? timestampDate(envelope.writtenAt).toISOString()
												: undefined,
										),
									},
									{
										term: "Changed by",
										value: (
											<UserLabel
												userId={envelope.writtenBy}
												fallback="A platform operator"
											/>
										),
									},
								]}
							/>
							<Disclosure title="Technical details">
								Module envelope revision {envelope.revision.toString()}, written
								by {envelope.writtenBy}.
							</Disclosure>
						</Section>
					)}

					<PermissionsEditor
						text={text}
						problem={draftProblem(text)}
						current={current}
						base={base}
						edited={draft !== null}
						saving={write.isPending}
						refusal={refusal?.from === "save" ? refusal.text : null}
						onDismissRefusal={() => setRefusal(null)}
						onChange={(next) => setDraft({ base, text: next })}
						onSave={() => setConfirmingSave(true)}
						onDiscard={() => {
							setDraft(null);
							setRefusal(null);
						}}
					/>
				</>
			)}

			<ConfirmDialog
				open={confirmingSave}
				onOpenChange={setConfirmingSave}
				title={saveQuestion(changes)}
				description={saveConsequences(changes)}
				confirmLabel="Save changes"
				pendingLabel="Saving…"
				destructive={changes.revoked.length > 0}
				isPending={write.isPending}
				onConfirm={() =>
					save(
						"save",
						{ expectedRevision: base, document: text },
						"Module permissions saved",
					)
				}
			/>

			{revoking && (
				<ConfirmDialog
					open
					onOpenChange={(open) => !open && setRevoking(null)}
					title={`Revoke approval for the module ${revoking}?`}
					description={`${revoking} will stop working for every organization on this platform within moments, and installations of it that are no longer permitted are removed. Work it has already finished is not undone. You can approve it again later.`}
					confirmLabel="Revoke approval"
					pendingLabel="Revoking…"
					isPending={write.isPending}
					onConfirm={() => {
						const next = withoutModule(document, current, revoking);
						if (next === null) return setRevoking(null);
						save(
							"revoke",
							{ expectedRevision: current, document: next },
							`Approval revoked for ${revoking}`,
						);
					}}
				/>
			)}
		</PageBody>
	);
}
