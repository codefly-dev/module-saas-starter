import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { NotificationSchema } from "@/gen/saas/accounts/v1/notifications_pb";
import { toNotification } from "./transforms";

describe("notification transforms", () => {
	it("maps transport timestamps, read state, and action URL", () => {
		const createdAt = new Date("2026-07-28T12:34:56.789Z");
		const readAt = new Date("2026-07-28T12:35:00.000Z");
		const message = create(NotificationSchema, {
			id: "notification-1",
			title: "You've been invited",
			body: "Join Acme",
			type: "info",
			actionUrl: "/invitations/accept?token=token",
			readAt: timestampFromDate(readAt),
			createdAt: timestampFromDate(createdAt),
		});

		expect(toNotification(message)).toEqual({
			id: "notification-1",
			title: "You've been invited",
			body: "Join Acme",
			type: "info",
			read: true,
			createdAt: createdAt.toISOString(),
			actionUrl: "/invitations/accept?token=token",
		});
	});

	it("maps an unread item without an action", () => {
		const message = create(NotificationSchema, {
			id: "notification-2",
			type: "security",
			actionUrl: "javascript:alert(document.cookie)",
		});

		expect(toNotification(message)).toMatchObject({
			id: "notification-2",
			type: "security",
			read: false,
			actionUrl: undefined,
		});
	});
});
