import { describe, expect, it } from "vitest";
import {
	backlogNotice,
	deliveryOrderLabel,
	eventVisibilityLabel,
	eventVisibilityTone,
	relayTiles,
} from "./event-labels";

describe("eventVisibilityLabel", () => {
	it("labels the declared catalog visibilities and undeclared types", () => {
		expect(
			["internal", "tenant", "external", ""].map(eventVisibilityLabel),
		).toEqual(["Internal", "Tenant", "External", "Undeclared"]);
	});

	it("reads an unrecognized visibility as Other, never the raw value", () => {
		expect(eventVisibilityLabel("restricted_v2")).toBe("Other");
		expect(eventVisibilityTone("restricted_v2")).toBe("neutral");
	});

	it("flags a type that was published without being declared", () => {
		expect(eventVisibilityTone("")).toBe("warning");
	});
});

describe("deliveryOrderLabel", () => {
	it("says the order in words", () => {
		expect(deliveryOrderLabel("ordered")).toBe("In order");
		expect(deliveryOrderLabel("unordered")).toBe("Any order");
	});

	it("does not echo a value it does not know", () => {
		expect(deliveryOrderLabel("at_least_once")).toBe("Not recognized");
	});
});

describe("relayTiles", () => {
	it("carries the three delivery counts", () => {
		expect(
			relayTiles({
				totalEvents: BigInt(12),
				publishedEvents: BigInt(9),
				backlog: BigInt(3),
			}),
		).toEqual([
			{ id: "recorded", label: "Events recorded", value: 12 },
			{ id: "sent", label: "Sent to subscribers", value: 9 },
			{ id: "waiting", label: "Waiting to be sent", value: 3 },
		]);
	});

	it("has no numbers, rather than zeros, when there is no answer", () => {
		expect(relayTiles(undefined).map((tile) => tile.value)).toEqual([
			null,
			null,
			null,
		]);
	});
});

describe("backlogNotice", () => {
	it("says nothing when nothing is waiting", () => {
		expect(backlogNotice(0, null)).toBeNull();
	});

	it("says how many events wait and since when", () => {
		expect(backlogNotice(1, null)).toBe(
			"Delivery is behind: 1 event is waiting to be sent.",
		);
		expect(backlogNotice(1200, "Jan 2, 2026, 03:04 AM")).toBe(
			"Delivery is behind: 1,200 events are waiting to be sent, the oldest since Jan 2, 2026, 03:04 AM.",
		);
	});
});
