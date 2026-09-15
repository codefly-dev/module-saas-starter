import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { NotificationSchema } from "@/gen/saas/accounts/v1/notifications_pb";
import { notificationActionUrl, toNotification } from "./transforms";

describe("notification transforms", () => {
	it("maps transport timestamps, read state, and actionability", () => {
		const createdAt = new Date("2026-07-28T12:34:56.789Z");
		const readAt = new Date("2026-07-28T12:35:00.000Z");
		const message = create(NotificationSchema, {
			id: "notification-1",
			title: "You've been invited",
			body: "Join Acme",
			type: "info",
			hasAction: true,
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
			hasAction: true,
		});
	});

	// The destination is a cache of a past grant, so the list must not carry it
	// even when the item is actionable.
	it("never carries a destination, only whether there is one", () => {
		const message = create(NotificationSchema, {
			id: "notification-4",
			actionUrl: "/invitations/accept?token=token",
			hasAction: true,
		});

		expect(toNotification(message)).not.toHaveProperty("actionUrl");
		expect(toNotification(message).hasAction).toBe(true);
	});

	it("maps an unread item without an action", () => {
		const message = create(NotificationSchema, {
			id: "notification-2",
			type: "security",
		});

		expect(toNotification(message)).toMatchObject({
			id: "notification-2",
			type: "security",
			read: false,
			hasAction: false,
		});
	});

	// The shape gate now guards the resolver's answer rather than the list, so it
	// is exercised directly.
	it.each([
		"//evil.example/path",
		"/\\evil.example/path",
		"https://evil.example/path",
		"javascript:alert(document.cookie)",
	])("rejects non-local action URL %s", (actionUrl) => {
		expect(notificationActionUrl(actionUrl)).toBeUndefined();
	});

	it("accepts a same-origin path with query and hash", () => {
		expect(notificationActionUrl("/invitations/accept?token=t#x")).toBe(
			"/invitations/accept?token=t#x",
		);
	});
});
