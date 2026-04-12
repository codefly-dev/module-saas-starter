"use client";

import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronUp } from "lucide-react";
import { useState } from "react";
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  Skeleton,
} from "@/shared/ui";
import { formatDate } from "@/shared/lib/utils";
import { webhookQueries } from "../service/queries";
import type { WebhookDelivery, WebhookSubscription } from "../model/types";
import { formatDeliveryStatus, formatEventType } from "../model/transforms";

interface WebhookDeliveriesPanelProps {
  subscription: WebhookSubscription;
  onClose: () => void;
}

export function WebhookDeliveriesPanel({
  subscription,
  onClose,
}: WebhookDeliveriesPanelProps) {
  const [expanded, setExpanded] = useState(true);
  const { data, isLoading } = useQuery(
    webhookQueries.deliveries(subscription.id),
  );

  const deliveries: WebhookDelivery[] = (
    data as { deliveries?: WebhookDelivery[] } | undefined
  )?.deliveries ?? [];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between py-3">
        <CardTitle className="text-base">
          Deliveries for{" "}
          <span className="font-mono text-sm">{subscription.url}</span>
        </CardTitle>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setExpanded(!expanded)}
          >
            {expanded ? (
              <ChevronUp className="h-4 w-4" />
            ) : (
              <ChevronDown className="h-4 w-4" />
            )}
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose}>
            Close
          </Button>
        </div>
      </CardHeader>

      {expanded && (
        <CardContent>
          {isLoading ? (
            <div className="space-y-2">
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : deliveries.length === 0 ? (
            <p className="text-sm text-muted-foreground py-4 text-center">
              No deliveries yet.
            </p>
          ) : (
            <div className="space-y-2">
              {deliveries.map((delivery) => {
                const { label, variant } = formatDeliveryStatus(
                  delivery.status,
                );
                return (
                  <div
                    key={delivery.id}
                    className="flex items-center justify-between rounded-md border p-3 text-sm"
                  >
                    <div className="flex items-center gap-3">
                      <Badge variant={variant}>{label}</Badge>
                      <span>{formatEventType(delivery.event)}</span>
                    </div>
                    <div className="flex items-center gap-4 text-muted-foreground">
                      {delivery.httpStatus != null && (
                        <span className="font-mono text-xs">
                          HTTP {delivery.httpStatus}
                        </span>
                      )}
                      <span className="text-xs">
                        {delivery.attempts} attempt
                        {delivery.attempts !== 1 ? "s" : ""}
                      </span>
                      <span className="text-xs">
                        {formatDate(delivery.lastAttemptAt)}
                      </span>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      )}
    </Card>
  );
}
