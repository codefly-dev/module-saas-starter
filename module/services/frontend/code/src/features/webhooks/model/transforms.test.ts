import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { WebhookSubscriptionSchema } from "@/gen/saas/accounts/v1/webhooks_pb";
import {
	formatDeliveryStatus,
	formatEventType,
	toWebhookSubscription,
} from "./transforms";

describe("webhook transforms", () => {
	it("converts generated timestamps into serializable domain values", () => {
		const createdAt = new Date("2026-07-13T12:34:56.789Z");
		const message = create(WebhookSubscriptionSchema, {
			id: "webhook-1",
			orgId: "org-1",
			url: "https://example.com/hooks",
			description: "Production events",
			events: ["user.created"],
			active: true,
			createdAt: timestampFromDate(createdAt),
		});

		expect(toWebhookSubscription(message)).toEqual({
			id: "webhook-1",
			orgId: "org-1",
			url: "https://example.com/hooks",
			description: "Production events",
			events: ["user.created"],
			active: true,
			createdAt: createdAt.toISOString(),
			updatedAt: undefined,
		});
	});

	it("keeps generated transport objects out of missing timestamps", () => {
		const message = create(WebhookSubscriptionSchema, { id: "webhook-2" });
		expect(toWebhookSubscription(message).createdAt).toBeUndefined();
	});

	it("formats event names and delivery states", () => {
		expect(formatEventType("team.member.added")).toBe("Team Member Added");
		expect(formatDeliveryStatus("failed")).toEqual({
			label: "Failed",
			variant: "destructive",
		});
	});
});
