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
			return create(AggregateAuditLogResponseSchema, {
				buckets: handler(request).map((bucket) => ({
					key: bucket.key,
					count: BigInt(bucket.count),
				})),
			});
		},
	};
	return { client, calls };
}
