"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	Clipboard,
	ClipboardCheck,
	Plus,
	Webhook as WebhookIcon,
} from "lucide-react";
import { useCallback, useState } from "react";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { OrgSelector } from "@/components/org-selector";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	Banner,
	Button,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Stack,
} from "@/shared/ui";
import { toWebhookSubscription } from "../model/transforms";
import type { WebhookSubscription } from "../model/types";
import { webhookMutations } from "../service/mutations";
import { webhookQueries } from "../service/queries";
import { WebhookDeliveriesPanel } from "./webhook-deliveries-panel";
import { WebhookForm } from "./webhook-form";
import { WebhooksTable } from "./webhooks-table";

export function WebhooksPage() {
	const { organizationId: orgId = "" } = useAuth();
	return <WebhooksPageForOrganization key={orgId} orgId={orgId} />;
}

function WebhooksPageForOrganization({ orgId }: { orgId: string }) {
	const queryClient = useQueryClient();
	const [showCreate, setShowCreate] = useState(false);
	const [confirmation, setConfirmation] = useState<{
		kind: "delete" | "rotate";
		webhook: WebhookSubscription;
	} | null>(null);
	const [selectedWebhook, setSelectedWebhook] =
		useState<WebhookSubscription | null>(null);
	const [revealedSecret, setRevealedSecret] = useState<string | null>(null);

	const {
		data: raw,
		isLoading,
		isError,
		error,
		refetch,
	} = useQuery({
		...webhookQueries.subscriptions(orgId),
		enabled: !!orgId,
	});
	// A refusal is not cured by asking again, so only a read that did not come
	// back offers the retry.
	const refused = readOutcome(isError, error) === "forbidden";
	const subscriptions: WebhookSubscription[] = (raw?.subscriptions ?? []).map(
		toWebhookSubscription,
	);

	const createMutation = useMutation({
		mutationFn: ({
			url,
			events,
			description,
		}: {
			url: string;
			events: string[];
			description?: string;
		}) => webhookMutations.create(orgId, url, events, description),
		onSuccess: (created: { secret?: string }) => {
			toast.success("Webhook created");
			queryClient.invalidateQueries({ queryKey: ["webhooks"] });
			setShowCreate(false);
			if (created.secret) setRevealedSecret(created.secret);
		},
		onError: (error) => toast.error(failureText("create the webhook", error)),
	});

	const deleteMutation = useMutation({
		mutationFn: (id: string) => webhookMutations.delete(id),
		onSuccess: () => {
			setConfirmation(null);
			toast.success("Webhook deleted");
			queryClient.invalidateQueries({ queryKey: ["webhooks"] });
			setSelectedWebhook(null);
		},
		onError: (error) => toast.error(failureText("delete the webhook", error)),
	});

	const testMutation = useMutation({
		mutationFn: (id: string) => webhookMutations.test(id),
		onSuccess: () => toast.success("Test delivery queued"),
		onError: (error) =>
			toast.error(failureText("send the test delivery", error)),
	});

	const rotateMutation = useMutation({
		mutationFn: (id: string) => webhookMutations.rotateSecret(id),
		onSuccess: (resp: { secret?: string }) => {
			setConfirmation(null);
			if (resp.secret) setRevealedSecret(resp.secret);
		},
		onError: (error) =>
			toast.error(failureText("rotate the signing secret", error)),
	});

	const handleTest = useCallback(
		(webhook: WebhookSubscription) => testMutation.mutate(webhook.id),
		[testMutation],
	);

	const handleDelete = useCallback(
		(webhook: WebhookSubscription) =>
			setConfirmation({ kind: "delete", webhook }),
		[],
	);
	const handleRotate = useCallback(
		(webhook: WebhookSubscription) =>
			setConfirmation({ kind: "rotate", webhook }),
		[],
	);
	const confirming = deleteMutation.isPending || rotateMutation.isPending;

	return (
		<PageBody>
			<PageHeader
				title="Webhooks"
				description="Send this organization's events to your own systems as they happen."
				actions={
					<>
						<OrgSelector />
						{orgId && (isLoading || subscriptions.length > 0) && !isError && (
							<Button onClick={() => setShowCreate(true)}>
								<Plus className="mr-2 h-4 w-4" />
								Create webhook
							</Button>
						)}
					</>
				}
			/>

			{!orgId ? (
				<EmptyState
					icon={WebhookIcon}
					title="Select an organization to view webhooks"
					description="Each webhook belongs to one organization. Pick an organization from the selector above to see or create its webhooks."
				/>
			) : isError ? (
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load the webhooks"
							detail={
								refused
									? "You don't have permission to see this organization's webhooks. Ask one of its administrators for access."
									: "Whether this organization has any is unknown. Try again; if it keeps failing, tell your administrator."
							}
						/>
						{!refused && (
							<Button
								variant="outline"
								size="sm"
								onClick={() => void refetch()}
							>
								Try again
							</Button>
						)}
					</Stack>
				</Panel>
			) : !isLoading && subscriptions.length === 0 ? (
				<EmptyState
					icon={WebhookIcon}
					title="Connect your first webhook"
					description="Send events to an endpoint you control. Create a webhook to choose which events it receives."
					action={
						<Button onClick={() => setShowCreate(true)}>Create webhook</Button>
					}
				/>
			) : (
				<WebhooksTable
					data={subscriptions}
					isLoading={isLoading}
					onTest={handleTest}
					onDelete={handleDelete}
					onSelect={setSelectedWebhook}
					onRotateSecret={handleRotate}
				/>
			)}

			{revealedSecret && (
				<RotatedSecretDialog
					secret={revealedSecret}
					onClose={() => setRevealedSecret(null)}
				/>
			)}

			{selectedWebhook && (
				<WebhookDeliveriesPanel
					subscription={selectedWebhook}
					onClose={() => setSelectedWebhook(null)}
				/>
			)}

			{showCreate && orgId && (
				<WebhookForm
					open
					onSubmit={(vals) => createMutation.mutate(vals)}
					onCancel={() => setShowCreate(false)}
					isPending={createMutation.isPending}
				/>
			)}
			<AlertDialog
				open={!!confirmation}
				onOpenChange={(open) => {
					if (!open && !confirming) setConfirmation(null);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>
							{confirmation?.kind === "delete"
								? "Delete webhook?"
								: "Rotate signing secret?"}
						</AlertDialogTitle>
						<AlertDialogDescription>
							<span className="break-all">{confirmation?.webhook.url}</span>{" "}
							{confirmation?.kind === "delete"
								? "This endpoint will stop receiving events. This cannot be undone."
								: "Deliveries will carry signatures for both the new and old keys for 24 hours. Update your endpoint to verify with the new key during that time."}
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel disabled={confirming}>Cancel</AlertDialogCancel>
						<AlertDialogAction
							variant="destructive"
							disabled={confirming}
							onClick={(event) => {
								event.preventDefault();
								if (!confirmation || confirming) return;
								if (confirmation.kind === "delete")
									deleteMutation.mutate(confirmation.webhook.id);
								else rotateMutation.mutate(confirmation.webhook.id);
							}}
						>
							{confirming
								? "Saving…"
								: confirmation?.kind === "delete"
									? "Delete webhook"
									: "Rotate secret"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</PageBody>
	);
}

/**
 * RotatedSecretDialog — modal shown immediately after a secret
 * rotation completes. The secret is displayed once, copy button
 * surfaced prominently, with an explicit warning that it cannot be
 * retrieved again. Closing the dialog forfeits the secret —
 * deliberate friction to make sure the operator copies it.
 */
function RotatedSecretDialog({
	secret,
	onClose,
}: {
	secret: string;
	onClose: () => void;
}) {
	const [copied, setCopied] = useState(false);

	async function copy() {
		try {
			await navigator.clipboard.writeText(secret);
			setCopied(true);
			toast.success("Secret copied");
		} catch {
			toast.error("Copy failed — copy it manually below");
		}
	}

	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-w-md">
				<DialogHeader>
					<DialogTitle>New signing secret</DialogTitle>
					<DialogDescription>
						Save this now — the secret is shown once and cannot be retrieved
						again. After rotation, the prior key remains in signatures for 24
						hours so you can deploy safely.
					</DialogDescription>
				</DialogHeader>

				<div className="rounded-md border bg-muted/30 p-3 font-mono text-xs break-all">
					{secret}
				</div>

				<Banner tone="warning">
					Update your endpoint to verify with this secret before deploying —
					incoming events are signed with it as of right now.
				</Banner>

				<DialogFooter>
					<Button variant="outline" onClick={copy}>
						{copied ? (
							<ClipboardCheck className="mr-2 h-4 w-4" />
						) : (
							<Clipboard className="mr-2 h-4 w-4" />
						)}
						{copied ? "Copied" : "Copy secret"}
					</Button>
					<Button onClick={onClose} disabled={!copied}>
						I&apos;ve saved it
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
