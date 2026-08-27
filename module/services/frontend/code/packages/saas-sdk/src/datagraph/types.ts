import type { MessageInitShape } from "@bufbuild/protobuf";
import type { CallOptions } from "@connectrpc/connect";
import type {
	AggregateAuditLogRequestSchema,
	AggregateAuditLogResponse,
} from "../gen/saas/accounts/v1/audit_pb.js";
import type { MetricBucket, MetricGroupBy } from "../schema.js";

/** One grouped value in a resolved metric. */
export interface MetricPoint {
	key: string;
	value: number;
}

/**
 * A resolved metric — the shape a widget renders. `groupBy`/`bucket` record the
 * dimension the points are keyed on, so a consumer can render the right axis and
 * a derived metric can refuse to combine dimensionally-incompatible inputs.
 */
export interface MetricSeries {
	metricId: string;
	points: MetricPoint[];
	total: number;
	groupBy: MetricGroupBy;
	bucket?: MetricBucket;
}

/** Org and time window a data graph resolves against. */
export interface MetricContext {
	orgId: string;
	from?: Date;
	to?: Date;
}

/** The bound, typed audit query a source metric compiles to. */
export type AuditAggregateQuery = MessageInitShape<
	typeof AggregateAuditLogRequestSchema
>;

/**
 * The slice of the audit Connect client the data-graph tooling needs. The
 * generated `SaasClient["audit"]` satisfies it structurally, and a test double
 * only has to implement one method.
 */
export interface AuditAggregateClient {
	aggregateAuditLog(
		request: AuditAggregateQuery,
		options?: CallOptions,
	): Promise<AggregateAuditLogResponse>;
}
