"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	AlertTriangle,
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
import { Button } from "@/shared/ui";
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
	const [selectedWebhook, setSelectedWebhook] =
		useState<WebhookSubscription | null>(null);
	const [revealedSecret, setRevealedSecret] = useState<string | null>(null);

	const { data: raw, isLoading } = useQuery({
		...webhookQueries.subscriptions(orgId),
		enabled: !!orgId,
	});
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
		onError: () => toast.error("Failed to create webhook"),
	});

	const deleteMutation = useMutation({
		mutationFn: (id: string) => webhookMutations.delete(id),
		onSuccess: () => {
			toast.success("Webhook deleted");
			queryClient.invalidateQueries({ queryKey: ["webhooks"] });
			setSelectedWebhook(null);
		},
		onError: () => toast.error("Failed to delete webhook"),
	});

	const testMutation = useMutation({
		mutationFn: (id: string) => webhookMutations.test(id),
		onSuccess: () => toast.success("Test delivery queued"),
		onError: () => toast.error("Failed to queue test delivery"),
	});

	const rotateMutation = useMutation({
		mutationFn: (id: string) => webhookMutations.rotateSecret(id),
		onSuccess: (resp: { secret?: string }) => {
			if (resp.secret) setRevealedSecret(resp.secret);
		},
		onError: (err) =>
			toast.error("Couldn't rotate secret", { description: err.message }),
	});

	const handleTest = useCallback(
		(webhook: WebhookSubscription) => testMutation.mutate(webhook.id),
		[testMutation],
	);

	const handleDelete = useCallback(
		(webhook: WebhookSubscription) => deleteMutation.mutate(webhook.id),
		[deleteMutation],
	);

	const handleRotate = useCallback(
		(webhook: WebhookSubscription) => {
			const ok = window.confirm(
				`Rotate signing secret for ${webhook.url}?\n\n` +
					"Deliveries will carry signatures for both the new and old keys for 24 hours. " +
					"Deploy the new verifier during that overlap.",
			);
			if (ok) rotateMutation.mutate(webhook.id);
		},
		[rotateMutation],
	);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<h2 className="text-2xl font-bold tracking-tight">Webhooks</h2>
				<div className="flex items-center gap-3">
					<OrgSelector />
					{orgId && (
						<Button onClick={() => setShowCreate(true)}>
							<Plus className="mr-2 h-4 w-4" />
							Create Webhook
						</Button>
					)}
				</div>
			</div>

			{!orgId ? (
				<EmptyState
					icon={WebhookIcon}
					title="Select an organization to view webhooks"
					description="Webhooks are scoped to one tenant at a time. Pick an org from the selector above to see (or create) its subscriptions."
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
		</div>
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

				<div className="flex items-start gap-2 rounded-md bg-amber-500/10 border border-amber-500/30 p-3 text-xs text-amber-900 dark:text-amber-200">
					<AlertTriangle className="h-4 w-4 shrink-0 mt-0.5" />
					<span>
						Update your endpoint to verify with this secret before deploying —
						incoming events are signed with it as of right now.
					</span>
				</div>

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
