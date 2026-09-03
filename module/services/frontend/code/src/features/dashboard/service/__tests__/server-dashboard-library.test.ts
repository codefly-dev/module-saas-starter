import type { JsonObject } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";
import {
	type Dashboard,
	DashboardVisibility as PbVisibility,
} from "@/gen/saas/accounts/v1/dashboards_pb";
import { DashboardNameError } from "../../model/record";
import { dashboard, metric } from "../../model/schema";
import { DashboardSpecError } from "../../model/validate";
import type { DashboardLibraryChange } from "../dashboard-library";
import { createServerDashboardLibrary } from "../server-dashboard-library";

const ORG = "11111111-1111-1111-1111-111111111111";
const OWNER = "22222222-2222-2222-2222-222222222222";

const specA = dashboard({
	title: "A",
	metrics: [metric({ title: "Top", groupBy: "event_type", chart: "bar" })],
});
const specB = dashboard({
	title: "B",
	metrics: [metric({ title: "By cat", groupBy: "category", chart: "bar" })],
});

// A minimal in-memory stand-in for the server: it applies the same
// server-assigned identity and mint-private semantics the real handler does, so
// the library's request/response mapping is what's under test.
function fakeClient() {
	const rows = new Map<string, Dashboard>();
	let seq = 0;
	const make = (fields: Partial<Dashboard>): Dashboard =>
		({
			id: fields.id || `id-${++seq}`,
			orgId: ORG,
			ownerId: OWNER,
			name: fields.name ?? "",
			spec: fields.spec,
			visibility: fields.visibility ?? PbVisibility.PRIVATE,
		}) as Dashboard;

	return {
		createDashboard: vi.fn(
			async (req: { id?: string; name: string; spec?: JsonObject }) => {
				const row = make({
					id: req.id || undefined,
					name: req.name,
					spec: req.spec,
				});
				rows.set(row.id, row);
				return row;
			},
		),
		getDashboard: vi.fn(async (req: { id: string }) => {
			const row = rows.get(req.id);
			if (!row) throw new ConnectError("not found", Code.NotFound);
			return row;
		}),
		listDashboards: vi.fn(async () => ({
			dashboards: [...rows.values()],
			nextPageToken: "",
		})),
		updateDashboard: vi.fn(
			async (req: { id: string; name?: string; spec?: JsonObject }) => {
				const row = rows.get(req.id);
				if (!row) throw new ConnectError("not found", Code.NotFound);
				if (req.name !== undefined) row.name = req.name;
				if (req.spec !== undefined) row.spec = req.spec;
				return row;
			},
		),
		deleteDashboard: vi.fn(async (req: { id: string }) => {
			rows.delete(req.id);
			return {};
		}),
		shareDashboard: vi.fn(
			async (req: { id: string; visibility: PbVisibility }) => {
				const row = rows.get(req.id);
				if (!row) throw new ConnectError("not found", Code.NotFound);
				row.visibility = req.visibility;
				return row;
			},
		),
	};
}

function libraryWith(client = fakeClient()) {
	// biome-ignore lint/suspicious/noExplicitAny: a partial typed fake stands in for the generated client.
	return { library: createServerDashboardLibrary(ORG, client as any), client };
}

describe("createServerDashboardLibrary", () => {
	it("creates a private record and drops server-only tenancy fields", async () => {
		const { library, client } = libraryWith();
		const record = await library.create({ name: "  Weekly  ", spec: specA });

		expect(client.createDashboard).toHaveBeenCalledWith({
			orgId: ORG,
			id: "",
			name: "Weekly",
			spec: specA,
		});
		expect(record).not.toHaveProperty("orgId");
		expect(record).not.toHaveProperty("ownerId");
		expect(record.visibility).toBe("private");
		expect(record.spec).toEqual(specA);
	});

	it("shares on create when visibility is org", async () => {
		const { library, client } = libraryWith();
		const record = await library.create({
			name: "Shared",
			spec: specA,
			visibility: "org",
		});
		expect(client.shareDashboard).toHaveBeenCalledTimes(1);
		expect(record.visibility).toBe("org");
	});

	it("rejects an empty name and an invalid spec before any RPC", async () => {
		const { library, client } = libraryWith();
		await expect(
			library.create({ name: "   ", spec: specA }),
		).rejects.toBeInstanceOf(DashboardNameError);
		await expect(
			library.create({
				name: "ok",
				spec: { version: 1, metrics: "nope" } as never,
			}),
		).rejects.toBeInstanceOf(DashboardSpecError);
		expect(client.createDashboard).not.toHaveBeenCalled();
	});

	it("returns null from get for a missing record", async () => {
		const { library } = libraryWith();
		expect(await library.get("missing")).toBeNull();
	});

	it("ensure returns the existing record and only creates when absent", async () => {
		const { library, client } = libraryWith();
		const first = await library.ensure({
			id: "pinned",
			name: "One",
			spec: specA,
		});
		expect(first.id).toBe("pinned");
		const second = await library.ensure({
			id: "pinned",
			name: "Two",
			spec: specB,
		});
		expect(second.id).toBe("pinned");
		expect(second.name).toBe("One");
		expect(client.createDashboard).toHaveBeenCalledTimes(1);
	});

	it("routes a name/spec patch to update and a visibility patch to share", async () => {
		const { library, client } = libraryWith();
		const created = await library.create({ name: "One", spec: specA });

		const renamed = await library.update(created.id, {
			name: "Renamed",
			spec: specB,
		});
		expect(client.updateDashboard).toHaveBeenCalledWith({
			id: created.id,
			name: "Renamed",
			spec: specB,
		});
		expect(renamed.name).toBe("Renamed");

		const shared = await library.update(created.id, { visibility: "org" });
		expect(client.shareDashboard).toHaveBeenCalledWith({
			id: created.id,
			visibility: PbVisibility.ORG,
		});
		expect(shared.visibility).toBe("org");
	});

	it("removes a record and lists the collection", async () => {
		const { library } = libraryWith();
		const a = await library.create({ name: "A", spec: specA });
		await library.create({ name: "B", spec: specB });
		expect(await library.list()).toHaveLength(2);
		await library.remove(a.id);
		expect(await library.list()).toHaveLength(1);
	});

	it("notifies subscribers with the collection after a mutation", async () => {
		const { library } = libraryWith();
		const changes: DashboardLibraryChange[] = [];
		const unsubscribe = library.subscribe((change) => changes.push(change));
		await library.create({ name: "A", spec: specA });
		expect(changes.at(-1)).toEqual({
			kind: "records",
			records: expect.arrayContaining([expect.objectContaining({ name: "A" })]),
		});
		unsubscribe();
	});

	it("deletes the just-created board when sharing it on create fails", async () => {
		const { library, client } = libraryWith();
		client.shareDashboard.mockRejectedValueOnce(
			new ConnectError("forbidden", Code.PermissionDenied),
		);
		await expect(
			library.create({ name: "Shared", spec: specA, visibility: "org" }),
		).rejects.toBeInstanceOf(ConnectError);
		// The private board created before the failed share must be cleaned up, not
		// left orphaned.
		expect(client.deleteDashboard).toHaveBeenCalledTimes(1);
		expect(await library.list()).toHaveLength(0);
	});

	it("list walks page tokens to aggregate every page", async () => {
		const dash = (id: string): Dashboard =>
			({
				id,
				orgId: ORG,
				ownerId: OWNER,
				name: id,
				visibility: PbVisibility.PRIVATE,
			}) as Dashboard;
		const client = {
			listDashboards: vi.fn(async (req: { pageToken?: string }) =>
				req.pageToken
					? { dashboards: [dash("c")], nextPageToken: "" }
					: { dashboards: [dash("a"), dash("b")], nextPageToken: "cursor-1" },
			),
		};
		// biome-ignore lint/suspicious/noExplicitAny: a partial typed fake stands in for the generated client.
		const library = createServerDashboardLibrary(ORG, client as any);
		const all = await library.list();
		expect(all.map((r) => r.id)).toEqual(["a", "b", "c"]);
		expect(client.listDashboards).toHaveBeenCalledTimes(2);
	});
});
