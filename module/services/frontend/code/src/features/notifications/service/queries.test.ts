import { beforeEach, describe, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@connectrpc/connect", () => ({
	createClient: () => ({ listNotifications: mocks.list }),
}));
vi.mock("@/lib/connect/transport", () => ({ apiTransport: {} }));
vi.mock("../model/transforms", () => ({
	toNotification: (value: unknown) => value,
}));
import { notificationQueries } from "./queries";

beforeEach(() => mocks.list.mockReset());
describe("notification queries", () => {
	it("continues past a page removed by visibility filtering", async () => {
		mocks.list
			.mockResolvedValueOnce({ notifications: [], nextPageToken: "next" })
			.mockResolvedValueOnce({
				notifications: [{ id: "visible" }],
				nextPageToken: "",
			});
		const query = notificationQueries.list(1, {
			orgId: "org-1",
			unreadOnly: true,
			firstVisible: true,
			userId: "user-1",
		});
		const result = await query.queryFn!({} as never);
		expect(result.notifications).toEqual([{ id: "visible" }]);
		expect(mocks.list).toHaveBeenNthCalledWith(2, {
			pageSize: 1,
			orgId: "org-1",
			unreadOnly: true,
			pageToken: "next",
		});
	});
	it("separates tenants, users and pages in cache", () => {
		const first = notificationQueries.list(1, {
			orgId: "org-1",
			userId: "user-1",
		}).queryKey;
		expect(first).not.toEqual(
			notificationQueries.list(1, { orgId: "org-2", userId: "user-1" })
				.queryKey,
		);
		expect(first).not.toEqual(
			notificationQueries.list(1, { orgId: "org-1", userId: "user-2" })
				.queryKey,
		);
		expect(first).not.toEqual(
			notificationQueries.list(1, {
				orgId: "org-1",
				userId: "user-1",
				pageToken: "next",
			}).queryKey,
		);
	});
});
