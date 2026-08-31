import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import * as audit from "../src/facade/audit.js";
import * as datasource from "../src/facade/datasource.js";
import * as webhooks from "../src/facade/webhooks.js";
import { AuditService } from "../src/gen/saas/accounts/v1/audit_pb.js";
import {
	DatasourceProvider,
	DatasourceService,
} from "../src/gen/saas/accounts/v1/datasource_pb.js";
import { WebhookService } from "../src/gen/saas/accounts/v1/webhooks_pb.js";

/**
 * A gateway that answers `AddGitHubSource` and records the org/repo it saw, so a
 * test proves the facade routes a call to the right procedure against a running
 * server rather than merely constructing an object.
 */
function gateway(): {
	transport: Transport;
	calls: Array<{ orgId: string; repo: string }>;
} {
	const calls: Array<{ orgId: string; repo: string }> = [];
	const transport = createRouterTransport(({ service }) => {
		service(DatasourceService, {
			addGitHubSource(req) {
				calls.push({ orgId: req.orgId, repo: req.repo });
				return {
					datasource: {
						id: "src_1",
						orgId: req.orgId,
						provider: DatasourceProvider.GITHUB,
						github: { repo: req.repo },
					},
				};
			},
		});
	});
	return { transport, calls };
}

describe("facade", () => {
	it("binds addGitHubSource to the gateway and resolves the typed response", async () => {
		const { transport, calls } = gateway();

		const res = await datasource.New(transport).addGitHubSource({
			orgId: "org_1",
			repo: "acme/widgets",
		});

		expect(calls).toEqual([{ orgId: "org_1", repo: "acme/widgets" }]);
		expect(res.datasource?.id).toBe("src_1");
		expect(res.datasource?.provider).toBe(DatasourceProvider.GITHUB);
		expect(res.datasource?.github?.repo).toBe("acme/widgets");
	});

	it("exposes each public service under its own gateway-bound facade", () => {
		const { transport } = gateway();

		expect(datasource.New(transport)).toHaveProperty("addGitHubSource");
		expect(webhooks.New(transport)).toHaveProperty("createSubscription");
		expect(audit.New(transport)).toHaveProperty("aggregateAuditLog");
	});

	it("keeps facades distinct — a call never crosses services", async () => {
		const { transport } = gateway();

		// The gateway only registers DatasourceService; a WebhookService call has
		// no handler and must surface as an error, not silently hit datasource.
		await expect(
			webhooks.New(transport).listSubscriptions({ orgId: "org_1" }),
		).rejects.toThrow();
	});

	it("wires the generated service descriptors", () => {
		expect(DatasourceService.typeName).toBe(
			"saas.accounts.v1.DatasourceService",
		);
		expect(WebhookService.typeName).toBe("saas.accounts.v1.WebhookService");
		expect(AuditService.typeName).toBe("saas.accounts.v1.AuditService");
	});
});
