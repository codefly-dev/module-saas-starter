"use client";

import { useState, useCallback } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/shared/ui";
import { webhookQueries } from "../service/queries";
import { webhookMutations } from "../service/mutations";
import type { WebhookSubscription } from "../model/types";
import { WebhooksTable } from "./webhooks-table";
import { WebhookForm } from "./webhook-form";
import { WebhookDeliveriesPanel } from "./webhook-deliveries-panel";

// TODO: Replace with real org selector when org context is available
const DEFAULT_ORG_ID = "default";

export function WebhooksPage() {
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [selectedWebhook, setSelectedWebhook] =
    useState<WebhookSubscription | null>(null);

  const { data: raw, isLoading } = useQuery(
    webhookQueries.subscriptions(DEFAULT_ORG_ID),
  );
  const subscriptions: WebhookSubscription[] =
    (raw as { subscriptions?: WebhookSubscription[] } | undefined)
      ?.subscriptions ?? [];

  const createMutation = useMutation({
    mutationFn: ({
      url,
      events,
      description,
    }: {
      url: string;
      events: string[];
      description?: string;
    }) => webhookMutations.create(DEFAULT_ORG_ID, url, events, description),
    onSuccess: () => {
      toast.success("Webhook created");
      queryClient.invalidateQueries({ queryKey: ["webhooks"] });
      setShowCreate(false);
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
    onSuccess: () => toast.success("Test delivery sent"),
    onError: () => toast.error("Failed to send test delivery"),
  });

  const handleTest = useCallback(
    (webhook: WebhookSubscription) => testMutation.mutate(webhook.id),
    [testMutation],
  );

  const handleDelete = useCallback(
    (webhook: WebhookSubscription) => deleteMutation.mutate(webhook.id),
    [deleteMutation],
  );

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-2xl font-bold tracking-tight">Webhooks</h2>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="mr-2 h-4 w-4" />
          Create Webhook
        </Button>
      </div>

      <WebhooksTable
        data={subscriptions}
        isLoading={isLoading}
        onTest={handleTest}
        onDelete={handleDelete}
        onSelect={setSelectedWebhook}
      />

      {selectedWebhook && (
        <WebhookDeliveriesPanel
          subscription={selectedWebhook}
          onClose={() => setSelectedWebhook(null)}
        />
      )}

      {showCreate && (
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
