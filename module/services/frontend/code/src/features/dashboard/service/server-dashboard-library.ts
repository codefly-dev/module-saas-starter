import type { JsonObject } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
	type Client,
	Code,
	ConnectError,
	createClient,
} from "@connectrpc/connect";
import {
	type Dashboard,
	DashboardListScope,
	DashboardService,
	DashboardVisibility as PbVisibility,
} from "@/gen/saas/accounts/v1/dashboards_pb";
import { apiTransport } from "@/lib/connect/transport";
import {
	assertDashboardName,
	type DashboardRecord,
	type DashboardRecordPatch,
	type DashboardVisibility,
} from "../model/record";
import type { DashboardDef } from "../model/schema";
import { assertDashboardSpec } from "../model/validate";
import type {
	CreateDashboardInput,
	DashboardLibrary,
	DashboardLibraryChange,
} from "./dashboard-library";

type DashboardClient = Client<typeof DashboardService>;

function toRecordVisibility(v: PbVisibility): DashboardVisibility {
	return v === PbVisibility.ORG ? "org" : "private";
}

function toProtoVisibility(v: DashboardVisibility): PbVisibility {
	return v === "org" ? PbVisibility.ORG : PbVisibility.PRIVATE;
}

function toIso(value: Dashboard["createdAt"]): string {
	return value ? timestampDate(value).toISOString() : new Date(0).toISOString();
}

// The wire record carries org_id/owner_id the client shape deliberately omits;
// tenancy and ownership are the server's to attest, not the browser's.
function toRecord(dashboard: Dashboard): DashboardRecord {
	return {
		id: dashboard.id,
		name: dashboard.name,
		spec: (dashboard.spec ?? {}) as unknown as DashboardDef,
		visibility: toRecordVisibility(dashboard.visibility),
		createdAt: toIso(dashboard.createdAt),
		updatedAt: toIso(dashboard.updatedAt),
	};
}

function isNotFound(error: unknown): boolean {
	return error instanceof ConnectError && error.code === Code.NotFound;
}

/**
 * The server-backed {@link DashboardLibrary} — the org-scoped, RLS-enforced
 * store the localStorage placeholder always named as its eventual replacement.
 * It drops in behind the same interface, so `MyDashboards` / `useDashboardLibrary`
 * are unchanged; an authenticated caller passes the viewer's `orgId`.
 *
 * `duplicate` (in the hook) is `create` with a copied spec; `share` is `update`
 * with a visibility. Both compose here rather than widening the RPC surface —
 * create always mints a private board and a visibility change is ShareDashboard.
 *
 * There is no server push channel yet, so `subscribe` re-reads the collection
 * after each local mutation and notifies; a change from another device lands on
 * the next read rather than live.
 */
export function createServerDashboardLibrary(
	orgId: string,
	client: DashboardClient = createClient(DashboardService, apiTransport),
): DashboardLibrary {
	const listeners = new Set<(change: DashboardLibraryChange) => void>();

	const emit = (change: DashboardLibraryChange) => {
		for (const listener of listeners) listener(change);
	};

	const refresh = async () => {
		try {
			emit({ kind: "records", records: await list() });
		} catch (error) {
			emit({ kind: "error", error: error as Error });
		}
	};

	const list = async (): Promise<DashboardRecord[]> => {
		// The RPC returns a bounded page; the collection contract returns them
		// all, so walk the page tokens to the end.
		const all: DashboardRecord[] = [];
		let pageToken = "";
		do {
			const response = await client.listDashboards({
				orgId,
				scope: DashboardListScope.UNSPECIFIED,
				pageToken,
			});
			for (const record of response.dashboards) all.push(toRecord(record));
			pageToken = response.nextPageToken;
		} while (pageToken !== "");
		return all;
	};

	const get = async (id: string): Promise<DashboardRecord | null> => {
		try {
			return toRecord(await client.getDashboard({ id }));
		} catch (error) {
			if (isNotFound(error)) return null;
			throw error;
		}
	};

	const create = async (
		input: CreateDashboardInput,
	): Promise<DashboardRecord> => {
		return insert("", input);
	};

	const insert = async (
		id: string,
		{ name, spec, visibility }: CreateDashboardInput,
	): Promise<DashboardRecord> => {
		assertDashboardName(name);
		assertDashboardSpec(spec);
		const created = await client.createDashboard({
			orgId,
			id,
			name: name.trim(),
			spec: spec as unknown as JsonObject,
		});
		let record = toRecord(created);
		if (visibility === "org") {
			// Create always mints a private board; sharing is a second, privileged
			// call. If it fails, delete the board just created so a rejected create
			// leaves nothing behind rather than an orphaned private record.
			try {
				record = toRecord(
					await client.shareDashboard({
						id: created.id,
						visibility: PbVisibility.ORG,
					}),
				);
			} catch (error) {
				try {
					await client.deleteDashboard({ id: created.id });
				} catch {}
				throw error;
			}
		}
		await refresh();
		return record;
	};

	const ensure = async (
		input: { id: string } & CreateDashboardInput,
	): Promise<DashboardRecord> => {
		const existing = await get(input.id);
		if (existing) return existing;
		const { id, ...rest } = input;
		return insert(id, rest);
	};

	const update = async (
		id: string,
		patch: DashboardRecordPatch,
	): Promise<DashboardRecord> => {
		if (patch.name !== undefined) assertDashboardName(patch.name);
		if (patch.spec !== undefined) assertDashboardSpec(patch.spec);

		let record: DashboardRecord | null = null;
		if (patch.name !== undefined || patch.spec !== undefined) {
			record = toRecord(
				await client.updateDashboard({
					id,
					name: patch.name?.trim(),
					spec: patch.spec as unknown as JsonObject | undefined,
				}),
			);
		}
		if (patch.visibility !== undefined) {
			record = toRecord(
				await client.shareDashboard({
					id,
					visibility: toProtoVisibility(patch.visibility),
				}),
			);
		}
		if (!record) {
			const current = await get(id);
			if (!current)
				throw new ConnectError("dashboard not found", Code.NotFound);
			record = current;
		}
		await refresh();
		return record;
	};

	const remove = async (id: string): Promise<void> => {
		await client.deleteDashboard({ id });
		await refresh();
	};

	return {
		list,
		get,
		create,
		ensure,
		update,
		remove,
		subscribe(listener) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	};
}
