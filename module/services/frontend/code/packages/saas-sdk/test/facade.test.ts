import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { accounts } from "../generated/typescript/src/accounts_facade.js";
import { AccessibleScopeService } from "../generated/typescript/src/gen/saas/accounts/v1/accessible_scopes_pb.js";
import { AuditService } from "../generated/typescript/src/gen/saas/accounts/v1/audit_pb.js";
import { DirectoryService } from "../generated/typescript/src/gen/saas/accounts/v1/directory_pb.js";
import {
	DatasourceProvider,
	DatasourceService,
} from "../generated/typescript/src/gen/saas/accounts/v1/datasource_pb.js";
import { WebhookService } from "../generated/typescript/src/gen/saas/accounts/v1/webhooks_pb.js";

interface Call {
	service: "datasource" | "webhook" | "audit" | "accessibleScope" | "directory";
	method: string;
	orgId?: string;
	repo?: string;
}

/**
 * A gateway that answers one method on each public service and records which
 * handler ran. Registering every service means a facade accessor bound to the
 * wrong service reaches the wrong handler (or none), so the recorded `service`
 * proves routing rather than mere object construction.
 */
function gateway(): { transport: Transport; calls: Call[] } {
	const calls: Call[] = [];
	const transport = createRouterTransport(({ service }) => {
		service(DatasourceService, {
			addGitHubSource(req) {
				calls.push({
					service: "datasource",
					method: "addGitHubSource",
					orgId: req.orgId,
					repo: req.repo,
				});
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
		service(WebhookService, {
			listSubscriptions() {
				calls.push({ service: "webhook", method: "listSubscriptions" });
				return {};
			},
		});
		service(AuditService, {
			aggregateAuditLog() {
				calls.push({ service: "audit", method: "aggregateAuditLog" });
				return {};
			},
		});
		service(AccessibleScopeService, {
			listMyAccessibleScopes(req) {
				calls.push({
					service: "accessibleScope",
					method: "listMyAccessibleScopes",
					orgId: req.orgId,
				});
				return {
					scopes: [
						{ nodeId: "node_1", scopePath: "root.a", kind: "collection" },
					],
				};
			},
		});
		service(DirectoryService, {
			listOrganizationMembers(req) {
				calls.push({
					service: "directory",
					method: "listOrganizationMembers",
					orgId: req.orgId,
				});
				return {
					members: [
						{
							orgId: req.orgId,
							userId: "user_1",
							userEmail: "jane.doe@example.com",
						},
					],
				};
			},
			listTeams(req) {
				calls.push({
					service: "directory",
					method: "listTeams",
					orgId: req.orgId,
				});
				return {
					teams: [{ id: "team_1", orgId: req.orgId, name: "Platform" }],
				};
			},
			listTeamMembers() {
				calls.push({ service: "directory", method: "listTeamMembers" });
				return { members: [{ teamId: "team_1", userId: "user_1" }] };
			},
		});
	});
	return { transport, calls };
}

describe("accounts facade", () => {
	it("exposes one accessor per generated service, each a callable client", () => {
		const { transport } = gateway();
		const client = accounts.New(transport);

		// The generated facade restricts to the services requested via
		// `--services`; each accessor returns a bound Connect client.
		expect(typeof client.accessibleScope).toBe("function");
		expect(typeof client.audit).toBe("function");
		expect(typeof client.datasource).toBe("function");
		expect(typeof client.directory).toBe("function");
		expect(typeof client.webhook).toBe("function");
	});

	it("binds addGitHubSource to the gateway and resolves the typed response", async () => {
		const { transport, calls } = gateway();

		const res = await accounts.New(transport).datasource().addGitHubSource({
			orgId: "org_1",
			repo: "acme/widgets",
		});

		expect(calls).toEqual([
			{
				service: "datasource",
				method: "addGitHubSource",
				orgId: "org_1",
				repo: "acme/widgets",
			},
		]);
		expect(res.datasource?.id).toBe("src_1");
		expect(res.datasource?.provider).toBe(DatasourceProvider.GITHUB);
		expect(res.datasource?.github?.repo).toBe("acme/widgets");
	});

	it("routes each accessor to its own service, never another", async () => {
		const { transport, calls } = gateway();
		const client = accounts.New(transport);

		// A call per accessor. If, say, `webhook()` bound DatasourceService, its
		// call would land on the datasource handler (or on a method that does not
		// exist), so the recorded service would be wrong or the call would throw.
		await client.datasource().addGitHubSource({
			orgId: "org_1",
			repo: "acme/widgets",
		});
		await client.webhook().listSubscriptions({ orgId: "org_1" });
		await client.audit().aggregateAuditLog({ orgId: "org_1" });
		await client.accessibleScope().listMyAccessibleScopes({
			orgId: "org_1",
			resourceType: "entry",
			action: "read",
		});
		const members = await client
			.directory()
			.listOrganizationMembers({ orgId: "org_1" });
		await client.directory().listTeams({ orgId: "org_1" });
		await client.directory().listTeamMembers({ teamId: "team_1" });

		expect(calls.map((call) => [call.service, call.method])).toEqual([
			["datasource", "addGitHubSource"],
			["webhook", "listSubscriptions"],
			["audit", "aggregateAuditLog"],
			["accessibleScope", "listMyAccessibleScopes"],
			["directory", "listOrganizationMembers"],
			["directory", "listTeams"],
			["directory", "listTeamMembers"],
		]);
		expect(members.members[0]?.userEmail).toBe("jane.doe@example.com");
	});
});
