import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { dashboard, metric } from "../../model/schema";
import {
	createMemoryDashboardLibrary,
	type DashboardLibrary,
} from "../dashboard-library";
import { useDashboardLibrary } from "../use-dashboard-library";

const specA = dashboard({
	title: "A",
	metrics: [metric({ title: "Top", groupBy: "event_type", chart: "bar" })],
});

function fixtures() {
	let tick = 0;
	let n = 0;
	return {
		now: () => `2026-08-31T00:00:${String(tick++).padStart(2, "0")}.000Z`,
		newId: () => `id-${++n}`,
	};
}

function mount(library: DashboardLibrary) {
	return renderHook(() => useDashboardLibrary("dashboard:hook-test", library));
}

describe("useDashboardLibrary", () => {
	it("loads an existing collection, newest first", async () => {
		const library = createMemoryDashboardLibrary([], fixtures());
		const first = await library.create({ name: "First", spec: specA });
		const second = await library.create({ name: "Second", spec: specA });

		const { result } = mount(library);
		await waitFor(() => expect(result.current.records).toHaveLength(2));
		expect(result.current.records.map((r) => r.id)).toEqual([
			second.id,
			first.id,
		]);
	});

	it("creates, renames, shares, duplicates, and removes", async () => {
		const library = createMemoryDashboardLibrary([], fixtures());
		const { result } = mount(library);

		let created!: { id: string };
		await act(async () => {
			created = await result.current.create({ name: "One", spec: specA });
		});
		await waitFor(() => expect(result.current.records).toHaveLength(1));

		await act(async () => {
			await result.current.rename(created.id, "Renamed");
		});
		await waitFor(() => expect(result.current.records[0].name).toBe("Renamed"));

		await act(async () => {
			await result.current.setVisibility(created.id, "org");
		});
		await waitFor(() =>
			expect(result.current.records[0].visibility).toBe("org"),
		);

		await act(async () => {
			await result.current.duplicate(created.id);
		});
		await waitFor(() => expect(result.current.records).toHaveLength(2));
		const copy = result.current.records.find(
			(r) => r.name === "Renamed (copy)",
		);
		expect(copy?.visibility).toBe("private");

		await act(async () => {
			await result.current.remove(created.id);
		});
		await waitFor(() => expect(result.current.records).toHaveLength(1));
	});

	it("resolves null when duplicating a missing dashboard", async () => {
		const library = createMemoryDashboardLibrary([], fixtures());
		const { result } = mount(library);
		let copy: unknown;
		await act(async () => {
			copy = await result.current.duplicate("missing");
		});
		expect(copy).toBeNull();
	});
});
