import { create } from "@bufbuild/protobuf";
import type {
	AuditAggregateClient,
	AuditAggregateQuery,
} from "../src/datagraph/types.js";
import { AggregateAuditLogResponseSchema } from "../src/gen/saas/accounts/v1/audit_pb.js";

export interface FakeBucket {
	key: string;
	count: number;
}

function response(buckets: FakeBucket[]) {
	return create(AggregateAuditLogResponseSchema, {
		buckets: buckets.map((bucket) => ({
			key: bucket.key,
			count: BigInt(bucket.count),
		})),
	});
}

/**
 * An in-memory audit client. `handler` maps each bound query to the buckets the
 * RPC would return, and every request is recorded for assertions.
 */
export function fakeAuditClient(
	handler: (request: AuditAggregateQuery) => FakeBucket[],
): { client: AuditAggregateClient; calls: AuditAggregateQuery[] } {
	const calls: AuditAggregateQuery[] = [];
	const client: AuditAggregateClient = {
		async aggregateAuditLog(request) {
			calls.push(request);
			return response(handler(request));
		},
	};
	return { client, calls };
}

/**
 * An audit client whose calls block until released, exposing how many are
 * in-flight at once — so a test can prove independent metrics fetch
 * concurrently rather than one after another.
 */
export function gatedAuditClient(
	handler: (request: AuditAggregateQuery) => FakeBucket[],
): {
	client: AuditAggregateClient;
	inFlight: () => number;
	releaseAll: () => void;
} {
	const releases: Array<() => void> = [];
	let active = 0;
	const client: AuditAggregateClient = {
		async aggregateAuditLog(request) {
			active++;
			await new Promise<void>((resolve) => releases.push(resolve));
			active--;
			return response(handler(request));
		},
	};
	return {
		client,
		inFlight: () => active,
		releaseAll: () => {
			for (const release of releases.splice(0)) release();
		},
	};
}
