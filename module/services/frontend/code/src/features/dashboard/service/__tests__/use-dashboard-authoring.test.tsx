import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAuditEventTypes } from "@/features/audit";
import { useAuth } from "@/lib/auth";
import { useAuditService } from "@/lib/hooks/use-api-client";
import { dashboard, metric } from "../../model/schema";
import { useDashboardAuthoring } from "../use-dashboard-authoring";

vi.mock("@/lib/hooks/use-api-client", () => ({
	useAuditService: vi.fn(),
}));
vi.mock("@/lib/auth", () => ({
	useAuth: vi.fn(),
}));

const initial = dashboard({
	title: "Initial",
	metrics: [
		metric({ title: "Top events", groupBy: "event_type", chart: "bar" }),
	],
});

// The projected vocabulary the shared cache should hold — identical whether it
// is read through the authoring surface or through useAuditEventTypes.
const vocab = [
	{
		name: "auth.login",
		version: 1,
		category: "authentication",
		owner: "accounts",
		deprecated: false,
		description: "A user logged in.",
	},
];

function fakeAuditService() {
	return {
		listAuditEventTypes: vi.fn(async () => ({
			$typeName: "saas.accounts.v1.ListAuditEventTypesResponse",
			types: [
				{
					$typeName: "saas.accounts.v1.AuditEventType",
					...vocab[0],
				},
			],
		})),
		aggregateAuditLog: vi.fn(),
	};
}

let audit: ReturnType<typeof fakeAuditService>;
let client: QueryClient;

function wrapper({ children }: { children: ReactNode }) {
	return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
	window.localStorage.clear();
	audit = fakeAuditService();
	client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	vi.mocked(useAuditService).mockReturnValue(
		audit as unknown as ReturnType<typeof useAuditService>,
	);
	vi.mocked(useAuth).mockReturnValue({
		organizationId: "org-1",
	} as unknown as ReturnType<typeof useAuth>);
});

afterEach(() => {
	vi.restoreAllMocks();
	window.localStorage.clear();
});

describe("useDashboardAuthoring", () => {
	it("backs the injected vocabulary read with react-query's cache, not a fresh RPC per call", async () => {
		const { result } = renderHook(
			() => useDashboardAuthoring("dashboard:test", initial),
			{ wrapper },
		);

		const reads = await act(() =>
			Promise.all([
				result.current.authoring.listEventTypes(),
				// A second read within staleTime is served from the cache, not refetched.
				result.current.authoring.listEventTypes(),
			]),
		);

		expect(reads[0].events).toEqual(vocab);
		expect(audit.listAuditEventTypes).toHaveBeenCalledTimes(1);
	});

	it("reads the same cache entry useAuditEventTypes reads — one key, one projection", async () => {
		const { result } = renderHook(
			() => useDashboardAuthoring("dashboard:test", initial),
			{ wrapper },
		);

		// Prime the shared cache through the imperative authoring reader.
		await act(async () => {
			await result.current.authoring.listEventTypes();
		});

		// The hook consumes the entry the reader populated: same projected shape,
		// no second fetch. If the projection lived in `select` instead of
		// `queryFn`, the reader would have cached the raw { types } response and
		// this equality would fail.
		const { result: hook } = renderHook(() => useAuditEventTypes(), {
			wrapper,
		});
		await waitFor(() => expect(hook.current.data).toBeDefined());

		expect(hook.current.data).toEqual(vocab);
		expect(audit.listAuditEventTypes).toHaveBeenCalledTimes(1);
	});
});
