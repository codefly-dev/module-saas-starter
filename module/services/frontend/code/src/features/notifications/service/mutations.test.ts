import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
	resolveNotificationAction: vi.fn(),
}));

vi.mock("@connectrpc/connect", () => ({
	createClient: () => ({
		resolveNotificationAction: mocks.resolveNotificationAction,
	}),
}));

vi.mock("@/lib/connect/transport", () => ({ apiTransport: {} }));

import { notificationMutations } from "./mutations";

describe("notificationMutations.resolveAction", () => {
	it("returns the server's destination when it is a local path", async () => {
		mocks.resolveNotificationAction.mockResolvedValue({
			actionUrl: "/docs/doc-1?page=2#top",
		});

		await expect(notificationMutations.resolveAction("n1")).resolves.toBe(
			"/docs/doc-1?page=2#top",
		);
	});

	// The destination feeds router.push. Resolving undefined for a rejected shape
	// would turn a bad stored URL into a click that silently does nothing, so the
	// shape gate has to reject rather than return an absent value.
	it.each([
		"https://evil.example/path",
		"//evil.example/path",
		"javascript:alert(document.cookie)",
		"",
	])("rejects a destination that is not a local path: %s", async (actionUrl) => {
		mocks.resolveNotificationAction.mockResolvedValue({ actionUrl });

		await expect(notificationMutations.resolveAction("n1")).rejects.toThrow(
			/not a local path/,
		);
	});
});
